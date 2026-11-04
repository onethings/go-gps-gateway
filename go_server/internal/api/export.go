package api

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

const (
	// exportPageSize is the largest page the position store returns. Exports
	// walk the requested window in pages so one statement never reads an
	// unbounded number of rows.
	exportPageSize = 1000
	// exportTimeout covers all pages of one export; the single query timeout
	// would cut a long track off.
	exportTimeout = 30 * time.Second
)

// exportFormat renders a position track in one of the formats the Java server
// offers under /api/positions.
type exportFormat struct {
	extension   string
	contentType string
	// deviceName reports whether the document embeds the device name, which
	// makes the handler load the device first.
	deviceName bool
	write      func(io.Writer, string, time.Time, time.Time, []model.Position) error
}

var (
	exportKML = exportFormat{
		extension:   "kml",
		contentType: "application/vnd.google-earth.kml+xml",
		deviceName:  true,
		write:       writeKML,
	}
	exportKMZ = exportFormat{
		extension:   "kmz",
		contentType: "application/vnd.google-earth.kmz",
		deviceName:  true,
		write:       writeKMZ,
	}
	exportCSV = exportFormat{
		extension:   "csv",
		contentType: "text/csv",
		write:       writeCSV,
	}
	exportGPX = exportFormat{
		extension:   "gpx",
		contentType: "application/gpx+xml",
		deviceName:  true,
		write:       writeGPX,
	}
)

// handlePositionExport serves /api/positions/kml, /kmz, /csv and /gpx. The
// parameters are the Java ones: deviceId is required, geofenceId, from and to
// are optional.
func (s *Server) handlePositionExport(format exportFormat) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.positions == nil {
			writeError(w, http.StatusNotImplemented, errors.New("positions are not configured"))
			return
		}
		if !s.authorize(w, r, auth.ScopeReadPositions) {
			return
		}

		query := r.URL.Query()
		deviceID, err := parseDeviceID(query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if deviceID == 0 {
			writeError(w, http.StatusBadRequest, errors.New("deviceId is required"))
			return
		}
		geofenceID, err := idQuery(query, "geofenceId")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		from, to, err := parseTimeRange(query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if geofenceID > 0 && s.geofences == nil {
			writeError(w, http.StatusNotImplemented, errors.New("geofences are not configured"))
			return
		}
		if !s.canAccessDevice(r, deviceID) {
			writeForbiddenDevice(w, deviceID)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), exportTimeout)
		defer cancel()

		fence, err := s.geofenceFilter(ctx, geofenceID)
		if errors.Is(err, model.ErrResourceNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown geofence %d", geofenceID))
			return
		}
		if err != nil {
			s.logf("api: export %s: load geofence %d: %v", format.extension, geofenceID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}

		deviceName := ""
		if format.deviceName {
			if s.devices == nil {
				writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
				return
			}
			device, err := s.devices.Device(ctx, deviceID)
			if errors.Is(err, model.ErrDeviceNotFound) {
				writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID))
				return
			}
			if err != nil {
				s.logf("api: export %s: load device %d: %v", format.extension, deviceID, err)
				writeError(w, http.StatusInternalServerError, errors.New("internal error"))
				return
			}
			deviceName = device.Name
		}

		positions, err := s.exportPositions(ctx, model.PositionFilter{
			DeviceID:  deviceID,
			From:      from,
			To:        to,
			Ascending: true,
		}, fence)
		if err != nil {
			s.logf("api: export %s: query positions: %v", format.extension, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}

		w.Header().Set("Content-Type", format.contentType)
		w.Header().Set(
			"Content-Disposition",
			fmt.Sprintf("attachment; filename=positions.%s", format.extension))
		if err := format.write(w, deviceName, from, to, positions); err != nil {
			// The headers are out; the log is the only place left to report
			// a half written document.
			s.logf("api: export %s: %v", format.extension, err)
		}
	}
}

// exportPositions walks the window in pages. The store clamps one query to a
// page, so the whole track is read a page at a time and capped at the same
// number of rows the Java report reads.
func (s *Server) exportPositions(ctx context.Context, filter model.PositionFilter, fence *geofence.Geofence) ([]model.Position, error) {
	positions := make([]model.Position, 0, exportPageSize)
	for offset := 0; ; offset += exportPageSize {
		// report.maxPositions caps the whole export; zero walks the window to
		// the end.
		limit := exportPageSize
		if s.reportMaxPositions > 0 {
			if remaining := s.reportMaxPositions - offset; remaining < limit {
				limit = remaining
			}
			if limit <= 0 {
				break
			}
		}

		page := filter
		page.Limit = limit
		page.Offset = offset

		batch, err := s.positions.QueryPositions(ctx, page)
		if err != nil {
			return nil, err
		}
		positions = append(positions, insideGeofence(batch, fence)...)
		if len(batch) < limit {
			break
		}
	}
	return positions, nil
}

const (
	kmlTimeFormat   = "2006-01-02 15:04"
	csvTimeFormat   = "2006-01-02 15:04:05"
	trackCoordinate = "%f,%f,%f"
)

type kmlDocument struct {
	XMLName  xml.Name   `xml:"kml"`
	Xmlns    string     `xml:"xmlns,attr"`
	Document kmlContent `xml:"Document"`
}

type kmlContent struct {
	Name      string       `xml:"name"`
	Placemark kmlPlacemark `xml:"Placemark"`
}

type kmlPlacemark struct {
	Name       string        `xml:"name"`
	LineString kmlLineString `xml:"LineString"`
}

type kmlLineString struct {
	Extrude      int    `xml:"extrude"`
	Tessellate   int    `xml:"tessellate"`
	AltitudeMode string `xml:"altitudeMode"`
	Coordinates  string `xml:"coordinates"`
}

func writeKML(w io.Writer, deviceName string, from, to time.Time, positions []model.Position) error {
	start, end := trackRange(from, to, positions)

	coordinates := make([]string, 0, len(positions))
	for _, position := range positions {
		coordinates = append(coordinates, fmt.Sprintf(
			trackCoordinate, position.Longitude, position.Latitude, position.Altitude))
	}

	document := kmlDocument{
		Xmlns: "http://www.opengis.net/kml/2.2",
		Document: kmlContent{
			Name: deviceName,
			Placemark: kmlPlacemark{
				Name: start.Format(kmlTimeFormat) + " - " + end.Format(kmlTimeFormat),
				LineString: kmlLineString{
					Extrude:      1,
					Tessellate:   1,
					AltitudeMode: "absolute",
					Coordinates:  strings.Join(coordinates, " "),
				},
			},
		},
	}
	return writeXML(w, document)
}

// trackRange prefers the requested window and falls back to the first and last
// position when a bound is missing.
func trackRange(from, to time.Time, positions []model.Position) (time.Time, time.Time) {
	start, end := from, to
	if start.IsZero() && len(positions) > 0 {
		start = positions[0].Time
	}
	if end.IsZero() && len(positions) > 0 {
		end = positions[len(positions)-1].Time
	}
	return start.UTC(), end.UTC()
}

type gpxDocument struct {
	XMLName xml.Name `xml:"gpx"`
	Version string   `xml:"version,attr"`
	Track   gpxTrack `xml:"trk"`
}

type gpxTrack struct {
	Name     string     `xml:"name"`
	Segments gpxSegment `xml:"trkseg"`
}

type gpxSegment struct {
	Points []gpxPoint `xml:"trkpt"`
}

type gpxPoint struct {
	Latitude  string `xml:"lat,attr"`
	Longitude string `xml:"lon,attr"`
	Elevation string `xml:"ele"`
	Time      string `xml:"time"`
}

func writeGPX(w io.Writer, deviceName string, _ time.Time, _ time.Time, positions []model.Position) error {
	points := make([]gpxPoint, 0, len(positions))
	for _, position := range positions {
		points = append(points, gpxPoint{
			Latitude:  fmt.Sprintf("%f", position.Latitude),
			Longitude: fmt.Sprintf("%f", position.Longitude),
			Elevation: fmt.Sprintf("%f", position.Altitude),
			Time:      position.Time.UTC().Format(time.RFC3339),
		})
	}

	document := gpxDocument{
		Version: "1.0",
		Track: gpxTrack{
			Name:     deviceName,
			Segments: gpxSegment{Points: points},
		},
	}
	return writeXML(w, document)
}

func writeXML(w io.Writer, document any) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	encoder := xml.NewEncoder(w)
	if err := encoder.Encode(document); err != nil {
		return err
	}
	return encoder.Flush()
}

func writeKMZ(w io.Writer, deviceName string, from, to time.Time, positions []model.Position) error {
	archive := zip.NewWriter(w)
	entry, err := archive.Create("doc.kml")
	if err != nil {
		return err
	}
	if err := writeKML(entry, deviceName, from, to, positions); err != nil {
		return err
	}
	return archive.Close()
}

// writeCSV follows the Java column order and appends one column per attribute
// key found in the track.
func writeCSV(w io.Writer, _ string, _ time.Time, _ time.Time, positions []model.Position) error {
	columns := []string{
		"id", "deviceId", "protocol", "serverTime", "deviceTime", "valid",
		"latitude", "longitude", "altitude", "speed", "course", "accuracy",
	}

	seen := make(map[string]struct{})
	for _, position := range positions {
		for key := range position.Attributes {
			seen[key] = struct{}{}
		}
	}
	attributes := make([]string, 0, len(seen))
	for key := range seen {
		attributes = append(attributes, key)
	}
	sort.Strings(attributes)
	columns = append(columns, attributes...)

	writer := csv.NewWriter(w)
	if err := writer.Write(columns); err != nil {
		return err
	}
	for _, position := range positions {
		row := []string{
			strconv.FormatInt(position.ID, 10),
			strconv.FormatInt(position.DeviceID, 10),
			sanitizeCSVCell(position.Protocol),
			position.ServerTime.UTC().Format(csvTimeFormat),
			position.Time.UTC().Format(csvTimeFormat),
			strconv.FormatBool(position.Valid),
			strconv.FormatFloat(position.Latitude, 'f', -1, 64),
			strconv.FormatFloat(position.Longitude, 'f', -1, 64),
			strconv.FormatFloat(position.Altitude, 'f', -1, 64),
			strconv.FormatFloat(position.Speed, 'f', -1, 64),
			strconv.FormatFloat(position.Course, 'f', -1, 64),
			strconv.FormatFloat(position.Accuracy, 'f', -1, 64),
		}
		for _, key := range attributes {
			row = append(row, attributeCell(position.Attributes[key]))
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

// sanitizeCSVCell trims a text cell and blanks the values a spreadsheet would
// execute as a formula, which is what the Java exporter does too.
func sanitizeCSVCell(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return ""
	default:
		return trimmed
	}
}

// attributeCell renders one attribute value for CSV. Only text goes through the
// formula guard; numbers and booleans are written as they are, like the Java
// implementation that only inspects String values.
func attributeCell(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return sanitizeCSVCell(typed)
	default:
		return fmt.Sprint(typed)
	}
}
