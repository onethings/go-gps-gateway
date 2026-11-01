package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/notify"
)

const maxNotificationBody = 1 << 16

// NotificationStore is the storage contract for the notification presets and
// per-user subscriptions.
type NotificationStore interface {
	Notifications(ctx context.Context) ([]model.Notification, error)
	Notification(ctx context.Context, notificationID int64) (*model.Notification, error)
	CreateNotification(ctx context.Context, create model.NotificationCreate) (*model.Notification, error)
	UpdateNotification(ctx context.Context, notificationID int64, update model.NotificationUpdate) (*model.Notification, error)
	DeleteNotification(ctx context.Context, notificationID int64) error
	UserNotifications(ctx context.Context, userID int64) ([]model.Notification, error)
	AssignNotification(ctx context.Context, userID, notificationID int64) error
	UnassignNotification(ctx context.Context, userID, notificationID int64) error
}

type notificationResponse struct {
	ID           int64          `json:"id"`
	Type         string         `json:"type"`
	Notificators []string       `json:"notificators"`
	Description  string         `json:"description,omitempty"`
	Always       bool           `json:"always"`
	CommandID    int64          `json:"commandId,omitempty"`
	CalendarID   int64          `json:"calendarId,omitempty"`
	Attributes   map[string]any `json:"attributes"`
	CreatedAt    time.Time      `json:"createdAt"`
}

type notificationsResponse struct {
	Notifications []notificationResponse `json:"notifications"`
	Count         int                    `json:"count"`
}

type userNotificationsResponse struct {
	UserID        int64                  `json:"userId"`
	Notifications []notificationResponse `json:"notifications"`
	Count         int                    `json:"count"`
}

type notificationCreateRequest struct {
	Type         string         `json:"type"`
	Notificators []string       `json:"notificators"`
	Description  string         `json:"description"`
	Always       bool           `json:"always"`
	CommandID    int64          `json:"commandId"`
	CalendarID   int64          `json:"calendarId"`
	Attributes   map[string]any `json:"attributes"`
}

// notificationUpdateRequest is a partial update: absent fields stay unchanged.
type notificationUpdateRequest struct {
	Type         *string         `json:"type"`
	Notificators *[]string       `json:"notificators"`
	Description  *string         `json:"description"`
	Always       *bool           `json:"always"`
	CommandID    *int64          `json:"commandId"`
	CalendarID   *int64          `json:"calendarId"`
	Attributes   *map[string]any `json:"attributes"`
}

func newNotificationResponse(notification model.Notification) notificationResponse {
	notificators := notification.Notificators
	if notificators == nil {
		notificators = []string{}
	}
	attributes := map[string]any(notification.Attributes)
	if attributes == nil {
		attributes = map[string]any{}
	}
	return notificationResponse{
		ID:           notification.ID,
		Type:         notification.Type,
		Notificators: notificators,
		Description:  notification.Description,
		Always:       notification.Always,
		CommandID:    notification.CommandID,
		CalendarID:   notification.CalendarID,
		Attributes:   attributes,
		CreatedAt:    notification.CreatedAt.UTC(),
	}
}

func (s *Server) notificationsConfigured(w http.ResponseWriter) bool {
	if s.notifications == nil {
		writeError(w, http.StatusNotImplemented, errors.New("notifications are not configured"))
		return false
	}
	return true
}

// requireSelfOrAdmin guards the subscription routes: an account may manage its
// own subscriptions, an admin may manage every account's.
func (s *Server) requireSelfOrAdmin(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if s.isAdmin(r) || s.isSelf(r, userID) {
		return true
	}
	writeError(w, http.StatusForbidden, errors.New("admin privileges or the own account are required"))
	return false
}

// normalizeNotificationType validates a preset type.
func normalizeNotificationType(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if !notify.ValidNotificationType(value) {
		return "", fmt.Errorf("unknown notification type %q", value)
	}
	return value, nil
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.notificationsConfigured(w) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	notifications, err := s.notifications.Notifications(ctx)
	if err != nil {
		s.logf("api: query notifications: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := notificationsResponse{
		Notifications: make([]notificationResponse, 0, len(notifications)),
		Count:         len(notifications),
	}
	for _, notification := range notifications {
		response.Notifications = append(response.Notifications, newNotificationResponse(notification))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.notificationsConfigured(w) {
		return
	}

	var request notificationCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	notificationType, err := normalizeNotificationType(request.Type)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	notificators, err := notify.ParseNotificators(request.Notificators)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	notification, err := s.notifications.CreateNotification(ctx, model.NotificationCreate{
		Type:         notificationType,
		Notificators: notificators,
		Description:  strings.TrimSpace(request.Description),
		Always:       request.Always,
		CommandID:    request.CommandID,
		CalendarID:   request.CalendarID,
		Attributes:   model.Attributes(request.Attributes),
	})
	if err != nil {
		s.logf("api: create notification: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	s.audit(r, model.ActionCreate, "notification", notification.ID)
	writeJSON(w, http.StatusCreated, newNotificationResponse(*notification))
}

func (s *Server) handleUpdateNotification(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.notificationsConfigured(w) {
		return
	}

	notificationID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var request notificationUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	update := model.NotificationUpdate{
		Always:     request.Always,
		CommandID:  request.CommandID,
		CalendarID: request.CalendarID,
	}
	if request.Attributes != nil {
		attributes := model.Attributes(*request.Attributes)
		update.Attributes = &attributes
	}
	if request.Type != nil {
		notificationType, err := normalizeNotificationType(*request.Type)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		update.Type = &notificationType
	}
	if request.Notificators != nil {
		notificators, err := notify.ParseNotificators(*request.Notificators)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		update.Notificators = &notificators
	}
	if request.Description != nil {
		description := strings.TrimSpace(*request.Description)
		update.Description = &description
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	notification, err := s.notifications.UpdateNotification(ctx, notificationID, update)
	if errors.Is(err, model.ErrNotificationNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown notification %d", notificationID))
		return
	}
	if err != nil {
		s.logf("api: update notification %d: %v", notificationID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	s.audit(r, model.ActionEdit, "notification", notification.ID)
	writeJSON(w, http.StatusOK, newNotificationResponse(*notification))
}

func (s *Server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.notificationsConfigured(w) {
		return
	}

	notificationID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if err := s.notifications.DeleteNotification(ctx, notificationID); err != nil {
		if errors.Is(err, model.ErrNotificationNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown notification %d", notificationID))
			return
		}
		s.logf("api: delete notification %d: %v", notificationID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.audit(r, model.ActionRemove, "notification", notificationID)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUserNotifications(w http.ResponseWriter, r *http.Request) {
	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.requireSelfOrAdmin(w, r, userID) ||
		!s.usersConfigured(w) || !s.notificationsConfigured(w) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if _, err := s.users.User(ctx, userID); err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}

	notifications, err := s.notifications.UserNotifications(ctx, userID)
	if err != nil {
		s.logf("api: query user %d notifications: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := userNotificationsResponse{
		UserID:        userID,
		Notifications: make([]notificationResponse, 0, len(notifications)),
		Count:         len(notifications),
	}
	for _, notification := range notifications {
		response.Notifications = append(response.Notifications, newNotificationResponse(notification))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAssignNotification(w http.ResponseWriter, r *http.Request) {
	s.handleNotificationSubscription(w, r, true)
}

func (s *Server) handleUnassignNotification(w http.ResponseWriter, r *http.Request) {
	s.handleNotificationSubscription(w, r, false)
}

// handleNotificationSubscription resolves the path, checks the caller and
// performs the PUT (assign) or DELETE (unassign).
func (s *Server) handleNotificationSubscription(w http.ResponseWriter, r *http.Request, assign bool) {
	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	notificationID, err := positiveID(r, "notificationId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.requireSelfOrAdmin(w, r, userID) ||
		!s.usersConfigured(w) || !s.notificationsConfigured(w) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if _, err := s.users.User(ctx, userID); err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}

	if assign {
		// Check the foreign key target so an unknown preset is a 404 instead
		// of a database error surfacing as 500.
		if _, err := s.notifications.Notification(ctx, notificationID); err != nil {
			if errors.Is(err, model.ErrNotificationNotFound) {
				writeError(w, http.StatusNotFound, fmt.Errorf("unknown notification %d", notificationID))
				return
			}
			s.logf("api: query notification %d: %v", notificationID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
	}

	if assign {
		err = s.notifications.AssignNotification(ctx, userID, notificationID)
	} else {
		err = s.notifications.UnassignNotification(ctx, userID, notificationID)
	}
	if err != nil {
		s.logf("api: update notification %d for user %d: %v", notificationID, userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	action := model.ActionLink
	if !assign {
		action = model.ActionUnlink
	}
	s.auditWithAttributes(r, action, "notification", notificationID,
		model.Attributes{"ownerType": "user", "ownerId": userID})

	w.WriteHeader(http.StatusNoContent)
}

// NotificationSender delivers a test message or an announcement through one
// configured channel. The command implements it over the channels it built; a
// deployment without it answers 501 on the test and send routes.
type NotificationSender interface {
	// SendTo delivers subject and body to one account through channel.
	SendTo(ctx context.Context, channel string, user model.User, subject, body string) error
	// Channels lists the channel names the deployment can deliver on.
	Channels() []string
}

// WithNotifierSender enables POST /api/notifications/test and
// /api/notifications/send.
func WithNotifierSender(sender NotificationSender) Option {
	return func(s *Server) { s.notifierSender = sender }
}

type notificationMessageRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// handleNotificationTest sends a test message to the caller through one channel,
// or through every configured channel when the path has no name.
func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	if s.notifierSender == nil {
		writeError(w, http.StatusNotImplemented, errors.New("notification channels are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteCommands) {
		return
	}

	var request notificationMessageRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
			return
		}
	}
	if request.Subject == "" {
		request.Subject = "Test message"
	}
	if request.Body == "" {
		request.Body = "This is a test notification from your Traccar server."
	}

	user, ok := s.callerUser(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	names := []string{r.PathValue("notificator")}
	if names[0] == "" {
		names = s.notifierSender.Channels()
	}
	for _, name := range names {
		if err := s.notifierSender.SendTo(ctx, name, user, request.Subject, request.Body); err != nil {
			s.logf("api: test notification %s: %v", name, err)
			writeError(w, http.StatusBadGateway, fmt.Errorf("%s: %w", name, err))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNotificationSend delivers an announcement to the selected accounts. The
// route is admin only because it reaches every account.
func (s *Server) handleNotificationSend(w http.ResponseWriter, r *http.Request) {
	if s.notifierSender == nil || s.users == nil {
		writeError(w, http.StatusNotImplemented, errors.New("notification channels are not configured"))
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	channel := r.PathValue("notificator")
	if channel == "" {
		writeError(w, http.StatusBadRequest, errors.New("a notificator is required"))
		return
	}

	var request notificationMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	if request.Subject == "" && request.Body == "" {
		writeError(w, http.StatusBadRequest, errors.New("subject or body is required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	userIDs, err := queryIDs(r.URL.Query()["userId"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	users, err := s.recipientUsers(ctx, userIDs)
	if err != nil {
		s.logf("api: list announcement recipients: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	for _, user := range users {
		if user.Temporary {
			continue
		}
		if err := s.notifierSender.SendTo(ctx, channel, user, request.Subject, request.Body); err != nil {
			s.logf("api: announcement to user %d: %v", user.ID, err)
			writeError(w, http.StatusBadGateway, err)
			return
		}
	}
	s.auditWithAttributes(r, model.ActionCommand, "notification", 0,
		model.Attributes{"type": channel})
	w.WriteHeader(http.StatusNoContent)
}

// callerUser loads the account behind the credential. The shared API key has no
// account, so it comes back as a zero user and the channel falls back to its
// server-wide targets.
func (s *Server) callerUser(w http.ResponseWriter, r *http.Request) (model.User, bool) {
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 || s.users == nil {
		return model.User{}, true
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	user, err := s.users.User(ctx, principal.UserID)
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, errors.New("unknown account"))
		return model.User{}, false
	}
	if err != nil {
		s.logf("api: load user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return model.User{}, false
	}
	return *user, true
}

// recipientUsers resolves the announcement recipients: the requested ids, or
// every account when the request names none.
func (s *Server) recipientUsers(ctx context.Context, userIDs []int64) ([]model.User, error) {
	if len(userIDs) > 0 {
		users := make([]model.User, 0, len(userIDs))
		for _, userID := range userIDs {
			user, err := s.users.User(ctx, userID)
			if errors.Is(err, model.ErrUserNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			users = append(users, *user)
		}
		return users, nil
	}
	return s.users.Users(ctx, model.UserFilter{Limit: maxUserLimit})
}

// queryIDs parses the repeated userId query parameter.
func queryIDs(values []string) ([]int64, error) {
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid userId %q", value)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
