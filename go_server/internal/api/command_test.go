package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
		response.Protocol != "gt06" || !response.Queued {
		t.Fatalf("response = %+v", response)
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
}

func TestCommandMethodNotAllowed(t *testing.T) {
	handler := NewServer(nil).Handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/commands", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}
