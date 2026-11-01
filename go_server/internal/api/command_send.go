package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// CommandCatalog is the optional capability of a CommandRouter that knows
// which command types one protocol can encode. The gateway router implements
// it; a router without it makes the types route fall back to every type the
// model knows.
type CommandCatalog interface {
	SupportedCommands(protocol string, textChannel bool) ([]string, bool)
}

// commandSendRequest is the body of POST /api/commands and /api/commands/send.
// groupId may also be passed as a query parameter, which is what the web
// interface does.
type commandSendRequest struct {
	DeviceID   int64          `json:"deviceId"`
	GroupID    int64          `json:"groupId"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

func (s *Server) commandsConfigured(w http.ResponseWriter) bool {
	if s.router == nil || s.protocols == nil {
		writeError(w, http.StatusNotImplemented, errors.New("commands are not configured"))
		return false
	}
	return true
}

// sendCommand encodes, records and delivers one command. It writes nothing to
// the response and returns the status the caller should use, so the single and
// the group routes share the whole path.
func (s *Server) sendCommand(
	ctx context.Context, r *http.Request, deviceID int64, commandType string, attributes map[string]any,
) (commandResponse, int, error) {
	protocol, found, err := s.protocols.DeviceProtocol(ctx, deviceID)
	if err != nil {
		s.logf("api: device protocol %d: %v", deviceID, err)
		return commandResponse{}, http.StatusInternalServerError, errors.New("internal error")
	}
	if !found {
		return commandResponse{}, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID)
	}
	if protocol == "" {
		return commandResponse{}, http.StatusConflict,
			errors.New("device protocol unknown; reconnect the device once")
	}

	command := model.NewCommand(deviceID, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}

	payload, err := s.router.EncodeCommand(protocol, command)
	if err != nil {
		if errors.Is(err, gateway.ErrUnknownProtocol) {
			s.logf("api: command %s for device %d: %v", commandType, deviceID, err)
			return commandResponse{}, http.StatusConflict, err
		}
		return commandResponse{}, http.StatusBadRequest, err
	}

	// The row exists before the payload does, so a crash between the two leaves
	// a pending command that was never sent rather than an untraceable send.
	if s.commands != nil {
		if err := s.commands.CreateCommand(ctx, command); err != nil {
			s.logf("api: record command %s for device %d: %v", commandType, deviceID, err)
			return commandResponse{}, http.StatusInternalServerError, errors.New("internal error")
		}
	}

	delivered := true
	if s.deliverer != nil {
		delivered, err = s.deliverer.Deliver(ctx, protocol, command, payload)
	} else if sendErr := s.router.Send(protocol, deviceID, payload); sendErr != nil {
		if s.commands != nil {
			s.markCommand(ctx, command.ID, false)
		}
		err = sendErr
	}

	if err != nil {
		switch {
		case errors.Is(err, gateway.ErrDeviceOffline):
			return commandResponse{}, http.StatusConflict, fmt.Errorf("device %d is offline", deviceID)
		case errors.Is(err, gateway.ErrCommandQueueFull):
			return commandResponse{}, http.StatusTooManyRequests,
				fmt.Errorf("device %d holds the maximum number of queued commands", deviceID)
		case errors.Is(err, gateway.ErrUnknownProtocol):
			s.logf("api: command %s for device %d: %v", commandType, deviceID, err)
			return commandResponse{}, http.StatusConflict, err
		default:
			s.logf("api: command %s for device %d: %v", commandType, deviceID, err)
			return commandResponse{}, http.StatusInternalServerError, errors.New("internal error")
		}
	}

	if s.deliverer == nil && s.commands != nil {
		s.markCommand(ctx, command.ID, true)
	}

	return commandResponse{
		ID:       command.ID,
		DeviceID: deviceID,
		Type:     commandType,
		Protocol: protocol,
		Queued:   !delivered,
	}, http.StatusAccepted, nil
}

// handleCommandSend serves POST /api/commands/send. With a groupId it fans the
// command out to every device of the group, which is the Java server's bulk
// path; without one it behaves like POST /api/commands.
func (s *Server) handleCommandSend(w http.ResponseWriter, r *http.Request) {
	if !s.commandsConfigured(w) {
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteCommands) {
		return
	}

	var request commandSendRequest
	// Extra fields are tolerated on purpose: Traccar clients send more than
	// deviceId/type/attributes.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	request.Type = strings.TrimSpace(request.Type)
	if request.Type == "" {
		writeError(w, http.StatusBadRequest, errors.New("type is required"))
		return
	}

	groupID := request.GroupID
	if raw := r.URL.Query().Get("groupId"); raw != "" {
		parsed, err := parsePositiveID(raw, "groupId")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		groupID = parsed
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if groupID > 0 {
		s.sendCommandToGroup(ctx, w, r, groupID, request)
		return
	}

	if request.DeviceID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("deviceId must be positive"))
		return
	}
	if !s.canAccessDevice(r, request.DeviceID) {
		writeForbiddenDevice(w, request.DeviceID)
		return
	}
	response, status, err := s.sendCommand(ctx, r, request.DeviceID, request.Type, request.Attributes)
	if err != nil {
		writeError(w, status, err)
		return
	}
	s.auditWithAttributes(r, model.ActionCommand, "device", request.DeviceID,
		model.Attributes{"type": request.Type})
	writeJSON(w, status, response)
}

// sendCommandToGroup sends one command to every accessible device of a group.
// A device that cannot be reached is reported in the failed list; the rest are
// still sent, which is what an operator expects from a broadcast.
func (s *Server) sendCommandToGroup(
	ctx context.Context, w http.ResponseWriter, r *http.Request, groupID int64, request commandSendRequest,
) {
	if s.permissions == nil {
		writeError(w, http.StatusNotImplemented, errors.New("permissions are not configured"))
		return
	}
	if err := s.checkObjectAccess(ctx, r, permissions.ObjectGroup, groupID); err != nil {
		s.writeResourceError(w, err)
		return
	}
	deviceIDs, err := s.permissions.GroupDeviceIDs(ctx, groupID)
	if err != nil {
		s.logf("api: devices of group %d: %v", groupID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if scope := s.deviceScope(r); scope != nil {
		deviceIDs = intersectDeviceIDs(scope, deviceIDs)
	}
	if len(deviceIDs) == 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("group %d has no accessible device", groupID))
		return
	}

	response := groupCommandResponse{Commands: make([]commandResponse, 0, len(deviceIDs))}
	for _, deviceID := range deviceIDs {
		sent, status, err := s.sendCommand(ctx, r, deviceID, request.Type, request.Attributes)
		if err != nil {
			response.Failed = append(response.Failed, groupCommandFailure{
				DeviceID: deviceID,
				Status:   status,
				Error:    err.Error(),
			})
			continue
		}
		response.Commands = append(response.Commands, sent)
	}
	response.Count = len(response.Commands)
	s.auditWithAttributes(r, model.ActionCommand, "group", groupID,
		model.Attributes{"type": request.Type})
	writeJSON(w, http.StatusOK, response)
}

type groupCommandFailure struct {
	DeviceID int64  `json:"deviceId"`
	Status   int    `json:"status"`
	Error    string `json:"error"`
}

type groupCommandResponse struct {
	Commands []commandResponse     `json:"commands"`
	Count    int                   `json:"count"`
	Failed   []groupCommandFailure `json:"failed,omitempty"`
}

// handleCommandBulk accepts either a list of commands or one command with a
// deviceIds list. It is the explicit bulk entry point; /api/commands/send with
// a groupId is the group flavoured one.
func (s *Server) handleCommandBulk(w http.ResponseWriter, r *http.Request) {
	if !s.commandsConfigured(w) {
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteCommands) {
		return
	}

	var batch struct {
		DeviceIDs  []int64              `json:"deviceIds"`
		Type       string               `json:"type"`
		Attributes map[string]any       `json:"attributes"`
		Commands   []commandSendRequest `json:"commands"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	type item struct {
		deviceID int64
		kind     string
		values   map[string]any
	}
	var items []item
	for _, command := range batch.Commands {
		if command.DeviceID <= 0 || strings.TrimSpace(command.Type) == "" {
			writeError(w, http.StatusBadRequest, errors.New("every command needs a deviceId and a type"))
			return
		}
		items = append(items, item{deviceID: command.DeviceID, kind: command.Type, values: command.Attributes})
	}
	if strings.TrimSpace(batch.Type) != "" {
		if len(batch.DeviceIDs) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("deviceIds is required with a type"))
			return
		}
		for _, deviceID := range batch.DeviceIDs {
			if deviceID <= 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid deviceId %d", deviceID))
				return
			}
			items = append(items, item{deviceID: deviceID, kind: batch.Type, values: batch.Attributes})
		}
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("no command to send"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	response := groupCommandResponse{Commands: make([]commandResponse, 0, len(items))}
	for _, entry := range items {
		if !s.canAccessDevice(r, entry.deviceID) {
			response.Failed = append(response.Failed, groupCommandFailure{
				DeviceID: entry.deviceID,
				Status:   http.StatusForbidden,
				Error:    "device is not accessible",
			})
			continue
		}
		sent, status, err := s.sendCommand(ctx, r, entry.deviceID, entry.kind, entry.values)
		if err != nil {
			response.Failed = append(response.Failed, groupCommandFailure{
				DeviceID: entry.deviceID,
				Status:   status,
				Error:    err.Error(),
			})
			continue
		}
		response.Commands = append(response.Commands, sent)
	}
	response.Count = len(response.Commands)
	s.audit(r, model.ActionCommand, "device", 0)
	writeJSON(w, http.StatusOK, response)
}

// handleCommandsSendList answers GET /api/commands/send. The Java server returns
// the saved command templates a user may send to the device. This server keeps
// command history only, so the list is empty; the types route is what feeds the
// picker.
func (s *Server) handleCommandsSendList(w http.ResponseWriter, r *http.Request) {
	if s.commands == nil {
		writeError(w, http.StatusNotImplemented, errors.New("commands are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadCommands) {
		return
	}
	deviceID, err := parseDeviceID(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if deviceID > 0 && !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

type commandTypeResponse struct {
	Type string `json:"type"`
}

func commandTypeList(types []string) []commandTypeResponse {
	sorted := append([]string(nil), types...)
	sort.Strings(sorted)
	response := make([]commandTypeResponse, 0, len(sorted))
	for _, commandType := range sorted {
		response = append(response, commandTypeResponse{Type: commandType})
	}
	return response
}

// handleCommandTypes answers GET /api/commands/types. With a deviceId the list
// is narrowed to what that device's protocol can encode on the chosen channel.
func (s *Server) handleCommandTypes(w http.ResponseWriter, r *http.Request) {
	if !s.commandsConfigured(w) {
		return
	}
	if !s.authorize(w, r, auth.ScopeReadCommands) {
		return
	}
	query := r.URL.Query()
	deviceID, err := parseDeviceID(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	textChannel, err := boolQuery(query, "textChannel")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if deviceID == 0 {
		writeJSON(w, http.StatusOK, commandTypeList(model.AllCommandTypes()))
		return
	}
	if !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	protocol, found, err := s.protocols.DeviceProtocol(ctx, deviceID)
	if err != nil {
		s.logf("api: device protocol %d: %v", deviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID))
		return
	}
	if protocol == "" {
		writeJSON(w, http.StatusOK, commandTypeList([]string{model.CommandCustom}))
		return
	}
	if catalog, ok := s.router.(CommandCatalog); ok {
		types, known := catalog.SupportedCommands(protocol, textChannel)
		if !known {
			writeError(w, http.StatusConflict, fmt.Errorf("unknown protocol %q", protocol))
			return
		}
		writeJSON(w, http.StatusOK, commandTypeList(types))
		return
	}
	writeJSON(w, http.StatusOK, commandTypeList(model.AllCommandTypes()))
}

// parsePositiveID parses one required positive id query parameter.
func parsePositiveID(raw, name string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}
