package schedule

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

type notice struct {
	recipient string
	subject   string
	body      string
}

type noticeRecorder struct {
	mu      sync.Mutex
	notices []notice
	err     error
}

func (r *noticeRecorder) SendNotice(_ context.Context, recipient, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.notices = append(r.notices, notice{recipient: recipient, subject: subject, body: body})
	return nil
}

func (r *noticeRecorder) snapshot() []notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notice(nil), r.notices...)
}

type noticeStore struct {
	users       []model.User
	devices     []model.Device
	deviceUsers map[int64][]model.User
	err         error
}

func (s *noticeStore) UsersExpiringBetween(_ context.Context, from, to time.Time) ([]model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	var users []model.User
	for _, user := range s.users {
		expires, ok := attributeExpiration(user.Attributes)
		if ok && expires.After(from) && !expires.After(to) {
			users = append(users, user)
		}
	}
	return users, nil
}

func (s *noticeStore) DevicesExpiringBetween(_ context.Context, from, to time.Time) ([]model.Device, error) {
	if s.err != nil {
		return nil, s.err
	}
	var devices []model.Device
	for _, device := range s.devices {
		if !device.ExpirationTime.IsZero() &&
			device.ExpirationTime.After(from) && !device.ExpirationTime.After(to) {
			devices = append(devices, device)
		}
	}
	return devices, nil
}

func (s *noticeStore) DeviceUsers(_ context.Context, deviceID int64) ([]model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.deviceUsers[deviceID], nil
}

func expiringUser(id int64, email string, expires time.Time) model.User {
	return model.User{
		ID:         id,
		Email:      email,
		Attributes: model.Attributes{"expirationTime": expires.UTC().Format(time.RFC3339)},
	}
}

func TestExpirationNoticeSendsAccountExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mailer := &noticeRecorder{}
	task := &ExpirationNotice{
		Store:  &noticeStore{users: []model.User{expiringUser(1, "alice@example.test", now.Add(-30*time.Minute))}},
		Mailer: mailer,
		Users:  true,
		Now:    func() time.Time { return now },
	}

	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sent := mailer.snapshot()
	if len(sent) != 1 {
		t.Fatalf("notices = %+v, want one", sent)
	}
	if sent[0].recipient != "alice@example.test" || sent[0].subject != "Account expiration" {
		t.Errorf("notice = %+v", sent[0])
	}
	if sent[0].body != "Your user account has expired." {
		t.Errorf("body = %q", sent[0].body)
	}
}

func TestExpirationNoticeSendsAccountReminderOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// The reminder boundary is expiration minus 24h, which falls 30 minutes
	// back, so this run is the one that sends it.
	mailer := &noticeRecorder{}
	task := &ExpirationNotice{
		Store: &noticeStore{users: []model.User{
			expiringUser(1, "alice@example.test", now.Add(23*time.Hour+30*time.Minute)),
		}},
		Mailer:       mailer,
		Users:        true,
		UserReminder: 24 * time.Hour,
		Now:          func() time.Time { return now },
	}

	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sent := mailer.snapshot()
	if len(sent) != 1 {
		t.Fatalf("notices = %+v, want one", sent)
	}
	if sent[0].subject != "Account expiration reminder" {
		t.Errorf("subject = %q", sent[0].subject)
	}
	if !strings.Contains(sent[0].body, now.Add(23*time.Hour+30*time.Minute).Format(noticeDateLayout)) {
		t.Errorf("body = %q, want the expiration date", sent[0].body)
	}

	// The next run is outside the window, so the reminder does not repeat.
	later := now.Add(time.Hour)
	task.Now = func() time.Time { return later }
	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := len(mailer.snapshot()); got != 1 {
		t.Errorf("notices after the second run = %d, want 1", got)
	}
}

func TestExpirationNoticeReachesDeviceUsers(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mailer := &noticeRecorder{}
	task := &ExpirationNotice{
		Store: &noticeStore{
			devices: []model.Device{{ID: 7, Name: "truck", ExpirationTime: now.Add(-time.Minute)}},
			deviceUsers: map[int64][]model.User{
				7: {
					{ID: 1, Email: "alice@example.test"},
					{ID: 2, Email: "bob@example.test", Disabled: true},
					{ID: 3, Email: ""},
				},
			},
		},
		Mailer:  mailer,
		Devices: true,
		Now:     func() time.Time { return now },
	}

	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sent := mailer.snapshot()
	if len(sent) != 1 {
		t.Fatalf("notices = %+v, want one for the enabled account with an address", sent)
	}
	if sent[0].recipient != "alice@example.test" || sent[0].subject != "Device expiration" {
		t.Errorf("notice = %+v", sent[0])
	}
	if sent[0].body != "Your device truck has expired." {
		t.Errorf("body = %q", sent[0].body)
	}
}

func TestExpirationNoticeReportsDeliveryFailures(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	task := &ExpirationNotice{
		Store:  &noticeStore{users: []model.User{expiringUser(1, "alice@example.test", now.Add(-time.Minute))}},
		Mailer: &noticeRecorder{err: errors.New("smtp down")},
		Users:  true,
		Now:    func() time.Time { return now },
	}

	err := task.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "smtp down") {
		t.Fatalf("Run error = %v, want the delivery failure", err)
	}
}

func TestExpirationNoticeWithoutMailerIsNoop(t *testing.T) {
	if err := (&ExpirationNotice{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
