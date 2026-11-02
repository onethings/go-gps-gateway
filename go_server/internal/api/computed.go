package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/model"
)

// computedTestRequest is the body of POST /api/attributes/computed/test. The
// Java server takes the device and the expression as query or form values; the
// JSON body is the shape traccar-web sends.
type computedTestRequest struct {
	DeviceID   int64  `json:"deviceId"`
	Expression string `json:"expression"`
	Attribute  string `json:"attribute"`
	Type       string `json:"type"`
}

// handleComputedTest evaluates one expression against the newest position of a
// device. It is the "test" button of the computed attribute dialog: the answer
// is the raw value, so the caller sees exactly what the chain would store.
func (s *Server) handleComputedTest(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadAttributes) {
		return
	}
	if s.computedAttributes == nil {
		notConfigured(w, "computed attributes")
		return
	}

	request, err := computedTestParameters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.DeviceID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("deviceId is required"))
		return
	}
	if strings.TrimSpace(request.Expression) == "" {
		writeError(w, http.StatusBadRequest, errors.New("expression is required"))
		return
	}
	if !s.canAccessDevice(r, request.DeviceID) {
		writeForbiddenDevice(w, request.DeviceID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	position := s.latestPosition(ctx, request.DeviceID)
	value, err := handler.EvaluateExpression(request.Expression, request.Type, position)
	if err != nil {
		// A bad expression is the caller's input, not a server fault.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func computedTestParameters(r *http.Request) (computedTestRequest, error) {
	var request computedTestRequest
	query := r.URL.Query()
	if raw := strings.TrimSpace(query.Get("deviceId")); raw != "" {
		if _, err := fmt.Sscan(raw, &request.DeviceID); err != nil {
			return request, errors.New("deviceId must be a number")
		}
	}
	request.Expression = query.Get("expression")
	request.Attribute = query.Get("attribute")
	request.Type = query.Get("type")

	if r.Method != http.MethodPost || request.Expression != "" {
		return request, nil
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxResourceBody)).Decode(&request); err != nil {
		return request, fmt.Errorf("invalid body: %w", err)
	}
	return request, nil
}

// latestPosition returns the newest stored position of a device, or an empty
// position when the device has never reported. An expression that only uses
// constants still evaluates instead of failing on a missing row.
func (s *Server) latestPosition(ctx context.Context, deviceID int64) *model.Position {
	position := model.NewPosition("")
	position.DeviceID = deviceID
	if s.latestPositions != nil {
		if positions, err := s.latestPositions.LatestPositions(ctx, []int64{deviceID}); err == nil &&
			len(positions) > 0 {
			position = &positions[0]
		}
		return position
	}
	if s.positions != nil {
		if positions, err := s.positions.QueryPositions(ctx, model.PositionFilter{
			DeviceID: deviceID,
			Limit:    1,
		}); err == nil && len(positions) > 0 {
			position = &positions[0]
		}
	}
	return position
}
