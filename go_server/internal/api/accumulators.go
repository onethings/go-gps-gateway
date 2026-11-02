package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// Accumulator keys, matching the names the Java web interface reads from the
// device accumulators dialog.
const (
	AccumulatorDistance = "totalDistance"
	AccumulatorHours    = "hours"
)

type accumulatorsResponse struct {
	TotalDistance float64 `json:"totalDistance"`
	Hours         float64 `json:"hours"`
}

// handleAccumulators answers GET /api/devices/{id}/accumulators. The values
// live on the device row, so a reset survives a restart and the reports can
// read the same source.
func (s *Server) handleAccumulators(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}
	device, ok := s.deviceFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, accumulatorsOf(device))
}

// handleAccumulatorsUpdate answers PUT /api/devices/{id}/accumulators. A
// missing field keeps its current value, which is what the reset dialog expects
// when it only clears the distance.
func (s *Server) handleAccumulatorsUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}
	device, ok := s.deviceFromPath(w, r)
	if !ok {
		return
	}

	var request struct {
		TotalDistance *float64 `json:"totalDistance"`
		Hours         *float64 `json:"hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxResourceBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	attributes := model.Attributes{}
	for key, value := range device.Attributes {
		attributes[key] = value
	}
	if request.TotalDistance != nil {
		attributes.Set(AccumulatorDistance, *request.TotalDistance)
	}
	if request.Hours != nil {
		attributes.Set(AccumulatorHours, *request.Hours)
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	updated, err := s.devices.UpdateDevice(ctx, device.ID, model.DeviceUpdate{
		Name:           device.Name,
		Model:          device.Model,
		Phone:          device.Phone,
		Contact:        device.Contact,
		Category:       device.Category,
		Disabled:       device.Disabled,
		ExpirationTime: device.ExpirationTime,
		CalendarID:     device.CalendarID,
		Attributes:     &attributes,
	})
	if err != nil {
		if errors.Is(err, model.ErrDeviceNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.logf("api: update accumulators of device %d: %v", device.ID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, accumulatorsOf(updated))
}

// accumulatorsOf renders the two values. Attributes are JSON, so a stored
// number may arrive as any numeric type; GetDouble handles all of them.
func accumulatorsOf(device *model.Device) accumulatorsResponse {
	if device == nil {
		return accumulatorsResponse{}
	}
	attributes := device.Attributes
	return accumulatorsResponse{
		TotalDistance: attributes.GetDouble(AccumulatorDistance, 0),
		Hours:         attributes.GetDouble(AccumulatorHours, 0),
	}
}
