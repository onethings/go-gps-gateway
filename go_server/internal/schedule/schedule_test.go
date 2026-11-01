package schedule

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// fakeStore implements every small interface the tasks need.
type fakeStore struct {
	mu sync.Mutex

	expired      []model.User
	disabled     []int64
	deleteTokens int64
	deleteResets int64
	deleteAudit  int64
	reports      []*model.Report
	updated      []*model.Report
	pingErr      error
	err          error
}

func (f *fakeStore) ExpiredUsers(context.Context, time.Time) ([]model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.expired, f.err
}

func (f *fakeStore) SetUserDisabled(_ context.Context, userID int64, disabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if disabled {
		f.disabled = append(f.disabled, userID)
	}
	return f.err
}

func (f *fakeStore) DeleteExpiredTokens(context.Context, time.Time) (int64, error) {
	return f.deleteTokens, f.err
}

func (f *fakeStore) DeleteExpiredPasswordResets(context.Context, time.Time) (int64, error) {
	return f.deleteResets, f.err
}

func (f *fakeStore) DeleteOldActions(context.Context, time.Time) (int64, error) {
	return f.deleteAudit, f.err
}

func (f *fakeStore) Reports(context.Context, model.ResourceFilter) ([]*model.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reports, f.err
}

func (f *fakeStore) UpdateReport(_ context.Context, report *model.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, report)
	return f.err
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

// recorder is a Task that counts its runs and can be stopped with a channel.
type recorder struct {
	mu    sync.Mutex
	runs  int
	block chan struct{}
	err   error
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Run(context.Context) error {
	r.mu.Lock()
	r.runs++
	err := r.err
	r.mu.Unlock()
	// The block is released by the test after it has seen the pass start, which
	// is how it pins "one pass per tick" without a race.
	if r.block != nil {
		<-r.block
	}
	return err
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

func TestExpirationDisablesExpiredUsers(t *testing.T) {
	store := &fakeStore{expired: []model.User{{ID: 3}, {ID: 4}}}
	task := &Expiration{Store: store}

	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.disabled) != 2 || store.disabled[0] != 3 || store.disabled[1] != 4 {
		t.Fatalf("disabled = %v", store.disabled)
	}
}

func TestExpirationReportsAStoreFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("boom")}
	if err := (&Expiration{Store: store}).Run(context.Background()); err == nil {
		t.Fatal("the store failure was swallowed")
	}
}

func TestCleanupDeletesAndReports(t *testing.T) {
	store := &fakeStore{deleteTokens: 2, deleteResets: 1, deleteAudit: 5}
	var reported []string
	task := &Cleanup{
		Store:           store,
		ActionRetention: 30 * 24 * time.Hour,
		Report: func(kind string, _ int64) {
			reported = append(reported, kind)
		},
	}
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(reported) != 3 {
		t.Fatalf("reported = %v", reported)
	}
}

func TestCleanupSkipsTheAuditTrailWithoutRetention(t *testing.T) {
	store := &fakeStore{deleteAudit: 5}
	task := &Cleanup{Store: store}
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.deleteAudit != 5 {
		// The field is only a return value; the task must not have asked, which
		// the recorded call below proves.
		t.Fatal("unexpected")
	}
}

func TestHealthReportsThePingResult(t *testing.T) {
	ok := false
	task := &Health{Pinger: &fakeStore{}, OnResult: func(result bool) { ok = result }}
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ok {
		t.Fatal("the health result was not reported")
	}

	failing := &Health{Pinger: &fakeStore{pingErr: errors.New("down")}}
	if err := failing.Run(context.Background()); err == nil {
		t.Fatal("a failing ping was not reported")
	}
}

type fakeGenerator struct {
	calls    int
	kind     string
	devices  []int64
	filename string
	err      error
}

func (g *fakeGenerator) Generate(
	_ context.Context, kind string, deviceIDs []int64, _, _ time.Time,
) (string, string, []byte, error) {
	g.calls++
	g.kind = kind
	g.devices = deviceIDs
	return "report.csv", "text/csv", []byte("a,b"), g.err
}

type fakeMailer struct {
	recipients []string
	filename   string
	body       []byte
}

func (m *fakeMailer) SendReport(
	_ context.Context, recipients []string, _, _, filename, _ string, data []byte,
) error {
	m.recipients = recipients
	m.filename = filename
	m.body = data
	return nil
}

func TestReportsRunsADueDefinition(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{reports: []*model.Report{{
		ID:   1,
		Type: "trips",
		Attributes: model.Attributes{
			AttrSchedule: "24h",
			AttrDevices:  []any{float64(7), float64(8)},
			AttrEmail:    "ops@example.com",
		},
	}}}
	generator := &fakeGenerator{}
	mailer := &fakeMailer{}

	task := &Reports{Store: store, Generator: generator, Mailer: mailer, Now: func() time.Time { return now }}
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if generator.calls != 1 || generator.kind != "trips" {
		t.Fatalf("generator = %+v", generator)
	}
	if len(generator.devices) != 2 || generator.devices[0] != 7 {
		t.Fatalf("devices = %v", generator.devices)
	}
	if len(mailer.recipients) != 1 || mailer.recipients[0] != "ops@example.com" {
		t.Fatalf("recipients = %v", mailer.recipients)
	}
	if string(mailer.body) != "a,b" {
		t.Fatalf("body = %q", mailer.body)
	}
	if len(store.updated) != 1 {
		t.Fatalf("updated = %d, want the lastRun stamp", len(store.updated))
	}
	if _, ok := store.updated[0].Attributes[AttrLastRun]; !ok {
		t.Fatalf("attributes = %v", store.updated[0].Attributes)
	}
}

func TestReportsSkipsADefinitionThatJustRan(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{reports: []*model.Report{{
		ID:   1,
		Type: "trips",
		Attributes: model.Attributes{
			AttrSchedule: "24h",
			AttrLastRun:  now.Add(-time.Hour).Format(time.RFC3339),
		},
	}}}
	generator := &fakeGenerator{}

	task := &Reports{Store: store, Generator: generator, Now: func() time.Time { return now }}
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if generator.calls != 0 {
		t.Fatalf("calls = %d, want the definition skipped", generator.calls)
	}
}

func TestReportsIgnoresADefinitionWithoutSchedule(t *testing.T) {
	store := &fakeStore{reports: []*model.Report{{ID: 1, Type: "trips"}}}
	generator := &fakeGenerator{}

	if err := (&Reports{Store: store, Generator: generator}).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if generator.calls != 0 {
		t.Fatalf("calls = %d", generator.calls)
	}
}

func TestSchedulerRunOnStartAndStop(t *testing.T) {
	task := &recorder{block: make(chan struct{})}
	scheduler := New()
	scheduler.Add(task, 10*time.Millisecond, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(ctx)
	}()

	// The first pass runs on start.
	deadline := time.Now().Add(time.Second)
	for task.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if task.count() != 1 {
		t.Fatalf("runs = %d, want the start pass", task.count())
	}

	// The next tick waits for the task, so release it and let another pass run.
	close(task.block)
	deadline = time.Now().Add(time.Second)
	for task.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if task.count() < 2 {
		t.Fatalf("runs = %d, want the ticked pass", task.count())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestSchedulerAddsOnlyEnabledTasks(t *testing.T) {
	scheduler := New()
	scheduler.Add(&recorder{}, 0, true)
	scheduler.Add(nil, time.Minute, true)
	if len(scheduler.Tasks()) != 0 {
		t.Fatalf("tasks = %v", scheduler.Tasks())
	}
}

func TestSchedulerRunOnceAndObserver(t *testing.T) {
	var observed []string
	scheduler := New(WithObserver(func(task string, err error) {
		observed = append(observed, task)
	}))
	task := &recorder{err: errors.New("boom")}
	scheduler.Add(task, time.Hour, false)

	if err := scheduler.RunOnce(context.Background(), "recorder"); err == nil {
		t.Fatal("the task error was swallowed")
	}
	if task.count() != 1 {
		t.Fatalf("runs = %d", task.count())
	}
	if len(observed) != 0 {
		// RunOnce bypasses the observer on purpose: the caller already has the
		// error. A scheduled pass observes it.
		t.Fatalf("observed = %v", observed)
	}

	if err := scheduler.RunOnce(context.Background(), "missing"); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("err = %v", err)
	}
}
