package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

type fakeSender struct {
	mu        sync.Mutex
	encoded   int
	sent      int
	protocol  string
	command   *model.Command
	encodeErr error
	sendErr   error
}

func (f *fakeSender) EncodeCommand(protocol string, command *model.Command) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.encoded++
	f.protocol = protocol
	f.command = command
	if f.encodeErr != nil {
		return nil, f.encodeErr
	}
	return []byte{0x01, 0x02, 0x03}, nil
}

func (f *fakeSender) Send(string, int64, []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent++
	return f.sendErr
}

type fakeDevices struct {
	protocol string
	found    bool
	err      error
}

func (f fakeDevices) DeviceProtocol(context.Context, int64) (string, bool, error) {
	return f.protocol, f.found, f.err
}

func post(t *testing.T, handler http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestCommandAccepted(t *testing.T) {
	sender := &fakeSender{}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{protocol: "gt06", found: true})).Handler()

	recorder := post(t, handler, "/api/commands",
		`{"deviceId":7,"type":"engineStop","attributes":{"frequency":30}}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body)
	}

	var response commandResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if response.DeviceID != 7 || response.Type != "engineStop" ||
		response.Protocol != "gt06" || response.Queued {
		t.Fatalf("response = %+v: without a dispatcher the payload is written before the answer", response)
	}

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.encoded != 1 || sender.sent != 1 {
		t.Fatalf("encoded = %d, sent = %d, want 1/1", sender.encoded, sender.sent)
	}
	if sender.protocol != "gt06" || sender.command.DeviceID != 7 {
		t.Fatalf("command = %+v, protocol = %q", sender.command, sender.protocol)
	}
	if value, ok := sender.command.GetAttribute(model.CmdKeyFrequency); !ok || value != float64(30) {
		t.Fatalf("frequency = %v, ok = %v", value, ok)
	}
}

func TestCommandValidation(t *testing.T) {
	sender := &fakeSender{}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{protocol: "gt06", found: true})).Handler()

	cases := map[string]string{
		"invalid json":    `{"deviceId":`,
		"missing device":  `{"type":"engineStop"}`,
		"negative device": `{"deviceId":-1,"type":"engineStop"}`,
		"missing type":    `{"deviceId":7}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if recorder := post(t, handler, "/api/commands", body); recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
			}
		})
	}

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.encoded != 0 {
		t.Fatalf("encoded = %d, want 0", sender.encoded)
	}
}

func TestCommandUnknownDevice(t *testing.T) {
	sender := &fakeSender{}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{found: false})).Handler()

	if recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCommandProtocolUnknown(t *testing.T) {
	sender := &fakeSender{}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{found: true})).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "reconnect") {
		t.Errorf("body = %s, want a hint about reconnecting", recorder.Body)
	}
}

func TestCommandOffline(t *testing.T) {
	sender := &fakeSender{sendErr: gateway.ErrDeviceOffline}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{protocol: "gt06", found: true})).Handler()

	if recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", recorder.Code, recorder.Body)
	}
}

// fakeDeliverer stands in for the dispatcher: it reports whether the payload
// reached the socket instead of writing it.
type fakeDeliverer struct {
	delivered bool
	err       error
	protocol  string
	command   *model.Command
	payload   []byte
	calls     int
}

func (f *fakeDeliverer) Deliver(
	_ context.Context, protocol string, command *model.Command, payload []byte,
) (bool, error) {
	f.calls++
	f.protocol = protocol
	f.command = command
	f.payload = payload
	return f.delivered, f.err
}

// TestCommandQueuedWhenTheDeviceIsAway pins the queue path: an offline device
// no longer fails the request, it gets a queued command and a 202.
func TestCommandQueuedWhenTheDeviceIsAway(t *testing.T) {
	sender := &fakeSender{}
	store := &recordingCommands{}
	deliverer := &fakeDeliverer{}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithCommandStore(store),
		WithCommandDeliverer(deliverer),
	).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body)
	}

	var response commandResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if !response.Queued || response.ID != 1 {
		t.Fatalf("response = %+v, want the recorded command marked as queued", response)
	}
	if deliverer.calls != 1 || deliverer.protocol != "gt06" || len(deliverer.payload) == 0 {
		t.Fatalf("deliverer = %+v", deliverer)
	}

	sender.mu.Lock()
	sent := sender.sent
	sender.mu.Unlock()
	if sent != 0 {
		t.Errorf("sent = %d, want 0: the dispatcher owns the write", sent)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sent) != 0 || len(store.failed) != 0 {
		t.Errorf("sent = %v, failed = %v: the dispatcher records the status, not the handler",
			store.sent, store.failed)
	}
}

func TestCommandQueueFullIsRefused(t *testing.T) {
	deliverer := &fakeDeliverer{err: gateway.ErrCommandQueueFull}
	handler := NewServer(nil,
		WithCommands(&fakeSender{}, fakeDevices{protocol: "gt06", found: true}),
		WithCommandDeliverer(deliverer),
	).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCommandUnsupported(t *testing.T) {
	sender := &fakeSender{encodeErr: gateway.ErrCommandUnsupported}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{protocol: "gt06", found: true})).Handler()

	if recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"positionSingle"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCommandStorageError(t *testing.T) {
	sender := &fakeSender{}
	handler := NewServer(nil, WithCommands(sender, fakeDevices{err: errors.New("boom")})).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "boom") {
		t.Errorf("body leaks the database error: %s", recorder.Body)
	}
}

func TestCommandNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	if recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`); recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", recorder.Code)
	}

	// Listing needs the store, which this server does not have either.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/commands", nil))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("list status = %d, want 501", recorder.Code)
	}
}

func TestCommandMethodNotAllowed(t *testing.T) {
	handler := NewServer(nil).Handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/commands", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

// recordingCommands is the CommandStore the command handler writes through. It
// keeps the calls in order, so a test can assert the status a row had at each
// step instead of only the final one.
type recordingCommands struct {
	mu        sync.Mutex
	created   []*model.Command
	sent      []int64
	failed    []int64
	createErr error
	queryErr  error
	results   []model.Command
	filter    model.CommandFilter
	queried   int
}

func (r *recordingCommands) CreateCommand(_ context.Context, command *model.Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.createErr != nil {
		return r.createErr
	}
	command.ID = int64(len(r.created) + 1)
	command.Status = model.CommandStatusPending
	r.created = append(r.created, command)
	return nil
}

func (r *recordingCommands) MarkCommandSent(_ context.Context, commandID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sent = append(r.sent, commandID)
	return nil
}

func (r *recordingCommands) MarkCommandFailed(_ context.Context, commandID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failed = append(r.failed, commandID)
	return nil
}

func (r *recordingCommands) QueryCommands(_ context.Context, filter model.CommandFilter) ([]model.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.queried++
	r.filter = filter
	return r.results, r.queryErr
}

func TestCommandIsRecorded(t *testing.T) {
	sender := &fakeSender{}
	store := &recordingCommands{}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithCommandStore(store),
	).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body)
	}

	var response commandResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if response.ID != 1 {
		t.Errorf("response id = %d, want 1", response.ID)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.created) != 1 {
		t.Fatalf("created = %d, want 1", len(store.created))
	}
	if store.created[0].Type != "engineStop" || store.created[0].DeviceID != 7 {
		t.Errorf("recorded = %+v", store.created[0])
	}
	if len(store.sent) != 1 || store.sent[0] != 1 {
		t.Errorf("sent = %v, want [1]", store.sent)
	}
	if len(store.failed) != 0 {
		t.Errorf("failed = %v, want none", store.failed)
	}
}

func TestCommandOfflineIsRecordedAsFailed(t *testing.T) {
	sender := &fakeSender{sendErr: gateway.ErrDeviceOffline}
	store := &recordingCommands{}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithCommandStore(store),
	).Handler()

	if recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", recorder.Code, recorder.Body)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.created) != 1 {
		t.Fatalf("created = %d, want 1: an offline send still leaves a row", len(store.created))
	}
	if len(store.failed) != 1 || store.failed[0] != 1 {
		t.Errorf("failed = %v, want [1]", store.failed)
	}
	if len(store.sent) != 0 {
		t.Errorf("sent = %v, want none", store.sent)
	}
}

func TestCommandUnsupportedIsNotRecorded(t *testing.T) {
	sender := &fakeSender{encodeErr: gateway.ErrCommandUnsupported}
	store := &recordingCommands{}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithCommandStore(store),
	).Handler()

	if recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"positionSingle"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.created) != 0 {
		t.Errorf("created = %d, want 0: an encoder rejection is not a command", len(store.created))
	}
}

func TestCommandIsNotSentWhenRecordingFails(t *testing.T) {
	sender := &fakeSender{}
	store := &recordingCommands{createErr: errors.New("insert failed")}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithCommandStore(store),
	).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "insert failed") {
		t.Errorf("body leaks the storage error: %s", recorder.Body)
	}

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.sent != 0 {
		t.Errorf("sent = %d, want 0: a command that cannot be recorded is not delivered", sender.sent)
	}
}

func TestCommandList(t *testing.T) {
	code := 1
	store := &recordingCommands{results: []model.Command{
		{
			ID:         9,
			DeviceID:   7,
			Type:       "engineStop",
			Attributes: map[string]any{"frequency": float64(30)},
			Status:     model.CommandStatusDone,
			ResultCode: &code,
			Serial:     42,
			Attempts:   3,
			CreatedAt:  time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC),
			SentAt:     time.Date(2026, 11, 1, 1, 0, 1, 0, time.UTC),
			DoneAt:     time.Date(2026, 11, 1, 1, 0, 2, 0, time.UTC),
		},
		{
			ID:         8,
			DeviceID:   7,
			Type:       "engineResume",
			Attributes: map[string]any{},
			Status:     model.CommandStatusPending,
			CreatedAt:  time.Date(2026, 11, 1, 0, 59, 0, 0, time.UTC),
		},
	}}
	handler := NewServer(nil, WithCommandStore(store)).Handler()

	recorder := get(t, handler, "/api/commands?deviceId=7&limit=10")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response commandsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if response.Count != 2 || len(response.Commands) != 2 {
		t.Fatalf("response = %+v", response)
	}

	newest := response.Commands[0]
	if newest.ID != 9 || newest.Status != model.CommandStatusDone {
		t.Errorf("newest = %+v", newest)
	}
	if newest.ResultCode == nil || *newest.ResultCode != 1 {
		t.Errorf("result code = %v, want 1", newest.ResultCode)
	}
	if newest.Serial != 42 || newest.Attempts != 3 {
		t.Errorf("serial = %d attempts = %d, want 42/3", newest.Serial, newest.Attempts)
	}
	if newest.Attributes["frequency"] != float64(30) {
		t.Errorf("attributes = %v", newest.Attributes)
	}
	if newest.SentAt == nil || newest.DoneAt == nil || newest.CreatedAt.IsZero() {
		t.Errorf("timestamps = %+v", newest)
	}

	pending := response.Commands[1]
	if pending.ResultCode != nil || pending.SentAt != nil || pending.DoneAt != nil {
		t.Errorf("pending command carries a finish: %+v", pending)
	}
	if pending.Attributes == nil {
		t.Error("pending attributes are null, want an object")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.filter.DeviceID != 7 || store.filter.Limit != 10 {
		t.Errorf("filter = %+v", store.filter)
	}
}

func TestCommandListIsScopedToOwnedDevices(t *testing.T) {
	store := &recordingCommands{}
	principal := auth.NewPrincipal(2, false,
		[]auth.Scope{auth.ScopeReadCommands}, []int64{7})
	handler := NewServer(nil, WithCommandStore(store), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/commands", ""); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if !reflect.DeepEqual(store.filter.DeviceIDs, []int64{7}) {
		t.Fatalf("device scope = %v, want [7]", store.filter.DeviceIDs)
	}
}

func TestCommandListNeedsScope(t *testing.T) {
	store := &recordingCommands{}
	principal := auth.NewPrincipal(2, false,
		[]auth.Scope{auth.ScopeWriteCommands}, []int64{7})
	handler := NewServer(nil, WithCommandStore(store), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/commands", ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.queried != 0 {
		t.Errorf("query ran without read:commands")
	}
}

// BenchmarkCommandAccepted measures the handler path with an in-memory store:
// decode the body, encode, record pending, send, record sent. It says nothing
// about the database round trips the real store adds.
func BenchmarkCommandAccepted(b *testing.B) {
	sender := &fakeSender{}
	store := &recordingCommands{}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithCommandStore(store),
	).Handler()

	body := []byte(`{"deviceId":7,"type":"engineStop","attributes":{"frequency":30}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		request := httptest.NewRequest(http.MethodPost, "/api/commands", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusAccepted {
			b.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
		}
	}
}
