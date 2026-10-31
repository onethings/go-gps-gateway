package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	Notificators []string  `json:"notificators"`
	Description  string    `json:"description,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
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
	Type         string   `json:"type"`
	Notificators []string `json:"notificators"`
	Description  string   `json:"description"`
}

// notificationUpdateRequest is a partial update: absent fields stay unchanged.
type notificationUpdateRequest struct {
	Type         *string   `json:"type"`
	Notificators *[]string `json:"notificators"`
	Description  *string   `json:"description"`
}

func newNotificationResponse(notification model.Notification) notificationResponse {
	notificators := notification.Notificators
	if notificators == nil {
		notificators = []string{}
	}
	return notificationResponse{
		ID:           notification.ID,
		Type:         notification.Type,
		Notificators: notificators,
		Description:  notification.Description,
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
	})
	if err != nil {
		s.logf("api: create notification: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

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

	update := model.NotificationUpdate{}
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

	w.WriteHeader(http.StatusNoContent)
}
