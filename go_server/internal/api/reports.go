package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
	"traccar-server_go/go_server/internal/report"
)

// xlsxContentType is what the Java report resource produces.
const xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// ReportMailer delivers a rendered report to one account.
type ReportMailer interface {
	SendReport(
		ctx context.Context, recipients []string, subject, body string,
		filename, contentType string, data []byte,
	) error
}

// WithReports enables the /api/reports/{name} endpoints. Without it they report
// 501; the /api/reports CRUD of saved report definitions keeps working.
func WithReports(service *report.Service) Option {
	return func(s *Server) { s.reports = service }
}

// WithReportMailer enables the "mail" flavour of a report export.
func WithReportMailer(mailer ReportMailer) Option {
	return func(s *Server) { s.reportMailer = mailer }
}

// reportQuery is the shared query string of every report.
type reportQuery struct {
	deviceIDs   []int64
	groupIDs    []int64
	geofenceIDs []int64
	from        time.Time
	to          time.Time
	daily       bool
	types       []string
	alarms      []string
}

// parseReportQuery reads the repeated ids and the required time range.
func parseReportQuery(r *http.Request) (reportQuery, error) {
	values := r.URL.Query()
	var query reportQuery

	var err error
	if query.deviceIDs, err = repeatedIDs(values, "deviceId"); err != nil {
		return query, err
	}
	if query.groupIDs, err = repeatedIDs(values, "groupId"); err != nil {
		return query, err
	}
	if query.geofenceIDs, err = repeatedIDs(values, "geofenceId"); err != nil {
		return query, err
	}
	if query.from, err = parseTime(values.Get("from")); err != nil {
		return query, fmt.Errorf("invalid from: %w", err)
	}
	if query.to, err = parseTime(values.Get("to")); err != nil {
		return query, fmt.Errorf("invalid to: %w", err)
	}
	if query.from.IsZero() || query.to.IsZero() {
		return query, errors.New("from and to are required")
	}
	if query.from.After(query.to) {
		return query, errors.New("from must not be after to")
	}
	if query.daily, err = boolQuery(values, "daily"); err != nil {
		return query, err
	}
	query.types = splitQueryValues(values["type"])
	query.alarms = splitQueryValues(values["alarm"])
	return query, nil
}

// repeatedIDs parses a repeated id parameter.
func repeatedIDs(values map[string][]string, name string) ([]int64, error) {
	raw, ok := values[name]
	if !ok {
		return nil, nil
	}
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		// A single parameter may carry a comma separated list, which is how the
		// Java client sends several ids.
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("invalid %s %q", name, part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func splitQueryValues(values []string) []string {
	var result []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}

// reportDeviceIDs expands the device and group filters into the device ids the
// report covers. A nil result means "every device the caller may see".
func (s *Server) reportDeviceIDs(
	ctx context.Context, r *http.Request, query reportQuery,
) ([]int64, error) {
	scope := s.deviceScope(r)
	allowed := func(id int64) bool { return scope == nil || reportContainsID(scope, id) }

	selected := make([]int64, 0)
	for _, deviceID := range query.deviceIDs {
		if allowed(deviceID) {
			selected = append(selected, deviceID)
		}
	}

	for _, groupID := range query.groupIDs {
		if s.permissions == nil {
			return nil, errors.New("permissions are not configured")
		}
		if err := s.checkObjectAccess(ctx, r, permissions.ObjectGroup, groupID); err != nil {
			return nil, err
		}
		ids, err := s.permissions.GroupDeviceIDs(ctx, groupID)
		if err != nil {
			return nil, err
		}
		for _, deviceID := range ids {
			if allowed(deviceID) {
				selected = append(selected, deviceID)
			}
		}
	}

	if len(query.deviceIDs) == 0 && len(query.groupIDs) == 0 {
		return scope, nil
	}
	return dedupeIDs(selected), nil
}

func reportContainsID(ids []int64, wanted int64) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func dedupeIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// reportAllowed applies the server restriction and the account restriction
// before a report runs.
func (s *Server) reportAllowed(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
	if s.serverSettings != nil {
		settings, err := s.serverSettings.ServerSettings(ctx)
		if err != nil {
			s.logf("api: report settings: %v", err)
		} else if settings.DisableReports {
			writeError(w, http.StatusForbidden, errors.New("reports are disabled"))
			return false
		}
	}
	principal := auth.FromContext(r.Context())
	if principal != nil && principal.UserID != 0 && s.users != nil {
		user, err := s.users.User(ctx, principal.UserID)
		if err == nil && model.Attributes(user.Attributes).GetBoolean("disableReports") {
			writeError(w, http.StatusForbidden, errors.New("reports are disabled for this account"))
			return false
		}
	}
	return true
}

// prepareReport parses and authorises one report request.
func (s *Server) prepareReport(
	w http.ResponseWriter, r *http.Request,
) (context.Context, context.CancelFunc, reportQuery, []int64, bool) {
	if s.reports == nil {
		writeError(w, http.StatusNotImplemented, errors.New("reports are not configured"))
		return nil, nil, reportQuery{}, nil, false
	}
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return nil, nil, reportQuery{}, nil, false
	}
	query, err := parseReportQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return nil, nil, reportQuery{}, nil, false
	}

	if err := s.reports.ValidateRange(query.from, query.to); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return nil, nil, reportQuery{}, nil, false
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	if !s.reportAllowed(ctx, w, r) {
		cancel()
		return nil, nil, reportQuery{}, nil, false
	}
	deviceIDs, err := s.reportDeviceIDs(ctx, r, query)
	if err != nil {
		cancel()
		s.writeResourceError(w, err)
		return nil, nil, reportQuery{}, nil, false
	}
	return ctx, cancel, query, deviceIDs, true
}

// writeReportError maps the report errors onto statuses.
func (s *Server) writeReportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, report.ErrPeriodTooLong), errors.Is(err, report.ErrTooManyPositions):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, permissions.ErrAccessDenied):
		writeError(w, http.StatusForbidden, err)
	default:
		s.logf("api: report: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
	}
}

// auditReport records the report request once per call.
func (s *Server) auditReport(r *http.Request, name string, query reportQuery) {
	s.auditWithAttributes(r, model.ActionReport, "report", 0, model.Attributes{
		"type":    name,
		"from":    query.from.UTC().Format(time.RFC3339),
		"to":      query.to.UTC().Format(time.RFC3339),
		"devices": fmt.Sprint(query.deviceIDs),
		"groups":  fmt.Sprint(query.groupIDs),
	})
}

func (s *Server) handleReportRoute(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	positions, err := s.reports.Route(ctx, deviceIDs, query.from, query.to)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "route", query)
	response := make([]report.PositionItem, 0, len(positions))
	for _, position := range positions {
		response = append(response, report.NewPositionItem(position))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleReportTrips(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	trips, err := s.reports.TripsReport(ctx, deviceIDs, query.from, query.to)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "trips", query)
	writeJSON(w, http.StatusOK, trips)
}

func (s *Server) handleReportStops(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	stops, err := s.reports.StopsReport(ctx, deviceIDs, query.from, query.to)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "stops", query)
	writeJSON(w, http.StatusOK, stops)
}

func (s *Server) handleReportSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	items, err := s.reports.SummaryReport(ctx, deviceIDs, query.from, query.to, query.daily)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "summary", query)
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleReportEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	items, err := s.reports.EventsReport(ctx, deviceIDs, query.from, query.to, query.types, query.alarms)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "events", query)
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleReportGeofences(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	items, err := s.reports.GeofencesReport(ctx, deviceIDs, query.geofenceIDs, query.from, query.to)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "geofences", query)
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleReportDevices(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		writeError(w, http.StatusNotImplemented, errors.New("reports are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	if !s.reportAllowed(ctx, w, r) {
		return
	}
	items, err := s.reports.DevicesReport(ctx, s.deviceScope(r))
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleReportCombined(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, query, deviceIDs, ok := s.prepareReport(w, r)
	if !ok {
		return
	}
	defer cancel()

	items, err := s.reports.CombinedReport(ctx, deviceIDs, query.from, query.to)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	s.auditReport(r, "combined", query)
	writeJSON(w, http.StatusOK, items)
}

// reportExport is the format of one export request.
type reportExport struct {
	name   string
	format string
	mail   bool
}

// exportRequest reads the format from the path and the mail flag from the query.
func exportRequest(r *http.Request) (reportExport, bool) {
	format := strings.ToLower(r.PathValue("type"))
	if format == "" {
		format = strings.ToLower(r.URL.Query().Get("format"))
	}
	mail, err := boolQuery(r.URL.Query(), "mail")
	if err != nil {
		format = ""
	}
	if format == "mail" {
		return reportExport{format: "xlsx", mail: true}, true
	}
	switch format {
	case "csv", "gpx", "kml", "xlsx":
		return reportExport{format: format, mail: mail}, true
	default:
		return reportExport{}, false
	}
}

// handleReportExport serves the CSV / GPX / KML / XLSX / mail flavour of a
// report. The report name comes from the path route the handler is mounted on,
// so one function serves every flavour.
func (s *Server) handleReportExport(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		export, ok := exportRequest(r)
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("format must be csv, gpx, kml, xlsx or mail"))
			return
		}
		export.name = name

		ctx, cancel, query, deviceIDs, prepared := s.prepareReport(w, r)
		if !prepared {
			return
		}
		defer cancel()

		if export.format == "gpx" || export.format == "kml" {
			if name != "route" {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%s has no %s export", name, export.format))
				return
			}
			positions, err := s.reports.Route(ctx, deviceIDs, query.from, query.to)
			if err != nil {
				s.writeReportError(w, err)
				return
			}
			var buffer bytes.Buffer
			if export.format == "gpx" {
				err = report.WriteRouteGPX(&buffer, name, positions)
			} else {
				err = report.WriteRouteKML(&buffer, positions)
			}
			if err != nil {
				s.writeReportError(w, err)
				return
			}
			contentType := "application/gpx+xml"
			if export.format == "kml" {
				contentType = "application/vnd.google-earth.kml+xml"
			}
			s.finishExport(ctx, w, r, export, query, deviceIDs,
				"report."+export.format, contentType, buffer.Bytes())
			return
		}

		table, err := s.reportTable(ctx, name, query, deviceIDs)
		if err != nil {
			s.writeReportError(w, err)
			return
		}
		var buffer bytes.Buffer
		if export.format == "csv" {
			err = report.WriteCSV(&buffer, table)
		} else {
			err = report.WriteXLSX(&buffer, []report.Sheet{{Name: name, Rows: tableRows(table)}})
		}
		if err != nil {
			s.writeReportError(w, err)
			return
		}
		contentType := "text/csv; charset=utf-8"
		if export.format == "xlsx" {
			contentType = xlsxContentType
		}
		filename := "report." + export.format
		if export.format == "xlsx" {
			filename = "report.xlsx"
		}
		s.finishExport(ctx, w, r, export, query, deviceIDs, filename, contentType, buffer.Bytes())
	}
}

// finishExport writes the export, or mails it when the request asked for that.
func (s *Server) finishExport(
	ctx context.Context, w http.ResponseWriter, r *http.Request, export reportExport,
	query reportQuery, deviceIDs []int64, filename, contentType string, data []byte,
) {
	s.auditReport(r, export.name, query)
	if export.mail {
		if s.reportMailer == nil {
			writeError(w, http.StatusNotImplemented, errors.New("report mail is not configured"))
			return
		}
		recipients, ok := s.reportMailRecipients(ctx, w, r)
		if !ok {
			return
		}
		err := s.reportMailer.SendReport(ctx, recipients,
			fmt.Sprintf("Traccar %s report", export.name),
			fmt.Sprintf("Report from %s to %s for %d device(s).",
				query.from.UTC().Format(time.RFC3339), query.to.UTC().Format(time.RFC3339), len(deviceIDs)),
			filename, contentType, data)
		if err != nil {
			s.logf("api: mail report: %v", err)
			writeError(w, http.StatusBadGateway, errors.New("could not send the report mail"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// reportMailRecipients resolves the account the report mail goes to.
func (s *Server) reportMailRecipients(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
) ([]string, bool) {
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 || s.users == nil {
		writeError(w, http.StatusBadRequest, errors.New("mailing a report needs an account"))
		return nil, false
	}
	user, err := s.users.User(ctx, principal.UserID)
	if err != nil || user.Email == "" {
		writeError(w, http.StatusBadRequest, errors.New("the account has no email address"))
		return nil, false
	}
	return []string{user.Email}, true
}

// tableRows converts a table to the any-typed rows of the XLSX writer.
func tableRows(table report.Table) [][]any {
	rows := make([][]any, 0, len(table.Rows)+1)
	header := make([]any, len(table.Header))
	for index, value := range table.Header {
		header[index] = value
	}
	rows = append(rows, header)
	for _, row := range table.Rows {
		cells := make([]any, len(row))
		for index, value := range row {
			cells[index] = value
		}
		rows = append(rows, cells)
	}
	return rows
}

// reportTable computes the table of a report for the CSV and XLSX exports.
func (s *Server) reportTable(
	ctx context.Context, name string, query reportQuery, deviceIDs []int64,
) (report.Table, error) {
	switch name {
	case "route":
		positions, err := s.reports.Route(ctx, deviceIDs, query.from, query.to)
		if err != nil {
			return report.Table{}, err
		}
		return report.RouteTable(positions), nil
	case "trips":
		items, err := s.reports.TripsReport(ctx, deviceIDs, query.from, query.to)
		if err != nil {
			return report.Table{}, err
		}
		return report.TripsTable(items), nil
	case "stops":
		items, err := s.reports.StopsReport(ctx, deviceIDs, query.from, query.to)
		if err != nil {
			return report.Table{}, err
		}
		return report.StopsTable(items), nil
	case "summary":
		items, err := s.reports.SummaryReport(ctx, deviceIDs, query.from, query.to, query.daily)
		if err != nil {
			return report.Table{}, err
		}
		return report.SummaryTable(items), nil
	case "events":
		items, err := s.reports.EventsReport(ctx, deviceIDs, query.from, query.to, query.types, query.alarms)
		if err != nil {
			return report.Table{}, err
		}
		return report.EventsTable(items), nil
	case "geofences":
		items, err := s.reports.GeofencesReport(ctx, deviceIDs, query.geofenceIDs, query.from, query.to)
		if err != nil {
			return report.Table{}, err
		}
		return report.GeofencesTable(items), nil
	case "devices":
		items, err := s.reports.DevicesReport(ctx, deviceIDs)
		if err != nil {
			return report.Table{}, err
		}
		return report.DevicesTable(items), nil
	default:
		return report.Table{}, fmt.Errorf("unknown report %q", name)
	}
}
