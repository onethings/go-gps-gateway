package api

import (
	"net/http"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// defaultNotificators is what a deployment can always deliver on: the mail
// channel and the webhook channel. Channels wired by the command are appended
// to the configured list.
var defaultNotificators = []string{"mail", "webhook"}

type notificatorResponse struct {
	Type string `json:"type"`
}

// handleNotificators lists the delivery channels an account may subscribe to.
// The Java server keeps this list in the NotificatorManager; here the command
// hands it over through WithNotificators.
func (s *Server) handleNotificators(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}
	names := s.notificators
	if len(names) == 0 {
		names = defaultNotificators
	}
	seen := make(map[string]struct{}, len(names))
	response := make([]notificatorResponse, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		response = append(response, notificatorResponse{Type: name})
	}
	writeJSON(w, http.StatusOK, response)
}

// handleNotificationTypes lists the event types a subscription may name, which
// is what the notification form picks from.
func (s *Server) handleNotificationTypes(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}
	types := append([]string{"all"}, model.EventTypes()...)
	response := make([]notificatorResponse, 0, len(types))
	for _, eventType := range types {
		response = append(response, notificatorResponse{Type: eventType})
	}
	writeJSON(w, http.StatusOK, response)
}
