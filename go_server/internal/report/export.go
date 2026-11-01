package report

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// csvSafe protects a spreadsheet from a text value that starts with the
// formula characters. The Java export does the same for its CSV downloads.
func csvSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

// Table is a rendered report: a header and the rows in display order. The CSV
// and XLSX writers share it, so a column only exists once per report.
type Table struct {
	Header []string
	Rows   [][]string
}

// WriteCSV writes one table.
func WriteCSV(w io.Writer, table Table) error {
	writer := csv.NewWriter(w)
	if err := writer.Write(table.Header); err != nil {
		return fmt.Errorf("report: csv header: %w", err)
	}
	for _, row := range table.Rows {
		for index := range row {
			row[index] = csvSafe(row[index])
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("report: csv row: %w", err)
		}
	}
	writer.Flush()
	return writer.Error()
}

// WriteCSVTable writes a table, which is the shared entry point of the report
// CSV exports.
func WriteCSVTable(w io.Writer, table Table) error { return WriteCSV(w, table) }

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatNumber(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

// RouteTable renders the route as one row per position.
func RouteTable(positions []model.Position) Table {
	table := Table{Header: []string{
		"id", "deviceId", "protocol", "deviceTime", "serverTime", "valid",
		"latitude", "longitude", "altitude", "speed", "course", "accuracy",
	}}
	for _, position := range positions {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(position.ID, 10),
			strconv.FormatInt(position.DeviceID, 10),
			position.Protocol,
			formatTime(position.Time),
			formatTime(position.ServerTime),
			strconv.FormatBool(position.Valid),
			formatNumber(position.Latitude),
			formatNumber(position.Longitude),
			formatNumber(position.Altitude),
			formatNumber(position.Speed),
			formatNumber(position.Course),
			formatNumber(position.Accuracy),
		})
	}
	return table
}

// TripsTable renders one row per trip.
func TripsTable(items []TripItem) Table {
	table := Table{Header: []string{
		"deviceId", "deviceName", "startTime", "endTime", "distance",
		"averageSpeed", "maxSpeed", "duration", "startAddress", "endAddress",
		"driverUniqueId", "driverName", "spentFuel",
	}}
	for _, item := range items {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(item.DeviceID, 10),
			item.DeviceName,
			formatTime(item.StartTime),
			formatTime(item.EndTime),
			formatNumber(item.Distance),
			formatNumber(item.AverageSpeed),
			formatNumber(item.MaxSpeed),
			strconv.FormatInt(item.Duration, 10),
			item.StartAddress,
			item.EndAddress,
			item.DriverUniqueID,
			item.DriverName,
			formatNumber(item.SpentFuel),
		})
	}
	return table
}

// StopsTable renders one row per stop.
func StopsTable(items []StopItem) Table {
	table := Table{Header: []string{
		"deviceId", "deviceName", "startTime", "endTime", "duration",
		"latitude", "longitude", "address", "engineHours", "spentFuel",
	}}
	for _, item := range items {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(item.DeviceID, 10),
			item.DeviceName,
			formatTime(item.StartTime),
			formatTime(item.EndTime),
			strconv.FormatInt(item.Duration, 10),
			formatNumber(item.Latitude),
			formatNumber(item.Longitude),
			item.Address,
			strconv.FormatInt(item.EngineHours, 10),
			formatNumber(item.SpentFuel),
		})
	}
	return table
}

// SummaryTable renders one row per device and day.
func SummaryTable(items []SummaryItem) Table {
	table := Table{Header: []string{
		"deviceId", "deviceName", "startTime", "endTime", "distance",
		"averageSpeed", "maxSpeed", "engineHours", "startOdometer", "endOdometer",
		"spentFuel",
	}}
	for _, item := range items {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(item.DeviceID, 10),
			item.DeviceName,
			formatTime(item.StartTime),
			formatTime(item.EndTime),
			formatNumber(item.Distance),
			formatNumber(item.AverageSpeed),
			formatNumber(item.MaxSpeed),
			strconv.FormatInt(item.EngineHours, 10),
			formatNumber(item.StartOdometer),
			formatNumber(item.EndOdometer),
			formatNumber(item.SpentFuel),
		})
	}
	return table
}

// EventsTable renders one row per event.
func EventsTable(items []EventItem) Table {
	table := Table{Header: []string{
		"id", "deviceId", "eventTime", "type", "geofenceId", "maintenanceId", "alarm",
	}}
	for _, item := range items {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(item.ID, 10),
			strconv.FormatInt(item.DeviceID, 10),
			formatTime(item.EventTime),
			item.Type,
			strconv.FormatInt(item.GeofenceID, 10),
			strconv.FormatInt(item.MaintenanceID, 10),
			model.Attributes(item.Attributes).GetString(model.KeyAlarm, ""),
		})
	}
	return table
}

// GeofencesTable renders one row per geofence interval.
func GeofencesTable(items []GeofenceItem) Table {
	table := Table{Header: []string{
		"deviceId", "deviceName", "geofenceId", "startTime", "endTime", "duration",
	}}
	for _, item := range items {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(item.DeviceID, 10),
			item.DeviceName,
			strconv.FormatInt(item.GeofenceID, 10),
			formatTime(item.StartTime),
			formatTime(item.EndTime),
			strconv.FormatInt(item.EndTime.Sub(item.StartTime).Milliseconds(), 10),
		})
	}
	return table
}

// DevicesTable renders one row per device.
func DevicesTable(items []DeviceItem) Table {
	table := Table{Header: []string{
		"deviceId", "deviceName", "uniqueId", "status", "lastUpdate",
	}}
	for _, item := range items {
		table.Rows = append(table.Rows, []string{
			strconv.FormatInt(item.Device.ID, 10),
			item.Device.Name,
			item.Device.UniqueID,
			item.Device.Status,
			formatTime(item.Device.LastUpdate),
		})
	}
	return table
}

// gpxHeader and gpxFooter keep the route export readable.
const gpxHeader = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="traccar-gateway" xmlns="http://www.topografix.com/GPX/1/1">
`

// WriteRouteGPX writes a GPX track of the route.
func WriteRouteGPX(w io.Writer, name string, positions []model.Position) error {
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if _, err := io.WriteString(w, gpxHeader); err != nil {
		return fmt.Errorf("report: gpx: %w", err)
	}
	start := xml.StartElement{Name: xml.Name{Local: "trk"}}
	if err := encoder.EncodeToken(start); err != nil {
		return fmt.Errorf("report: gpx: %w", err)
	}
	if name != "" {
		if err := encoder.EncodeElement(name, xml.StartElement{Name: xml.Name{Local: "name"}}); err != nil {
			return fmt.Errorf("report: gpx: %w", err)
		}
	}
	segment := xml.StartElement{Name: xml.Name{Local: "trkseg"}}
	if err := encoder.EncodeToken(segment); err != nil {
		return fmt.Errorf("report: gpx: %w", err)
	}
	for _, position := range positions {
		point := xml.StartElement{
			Name: xml.Name{Local: "trkpt"},
			Attr: []xml.Attr{
				{Name: xml.Name{Local: "lat"}, Value: formatNumber(position.Latitude)},
				{Name: xml.Name{Local: "lon"}, Value: formatNumber(position.Longitude)},
			},
		}
		if err := encoder.EncodeToken(point); err != nil {
			return fmt.Errorf("report: gpx: %w", err)
		}
		if err := encoder.EncodeElement(formatTime(position.Time),
			xml.StartElement{Name: xml.Name{Local: "time"}}); err != nil {
			return fmt.Errorf("report: gpx: %w", err)
		}
		if err := encoder.EncodeToken(point.End()); err != nil {
			return fmt.Errorf("report: gpx: %w", err)
		}
	}
	for _, end := range []xml.EndElement{{Name: segment.Name}, {Name: start.Name}} {
		if err := encoder.EncodeToken(end); err != nil {
			return fmt.Errorf("report: gpx: %w", err)
		}
	}
	if err := encoder.Flush(); err != nil {
		return fmt.Errorf("report: gpx: %w", err)
	}
	_, err := io.WriteString(w, "</gpx>\n")
	if err != nil {
		return fmt.Errorf("report: gpx: %w", err)
	}
	return nil
}

// WriteRouteKML writes a KML document with one placemark line per device.
func WriteRouteKML(w io.Writer, positions []model.Position) error {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	builder.WriteString(`<kml xmlns="http://www.opengis.net/kml/2.2">` + "\n<Document>\n")
	builder.WriteString("<name>Route</name>\n")

	byDevice := make(map[int64][]model.Position)
	order := make([]int64, 0)
	for _, position := range positions {
		if _, seen := byDevice[position.DeviceID]; !seen {
			order = append(order, position.DeviceID)
		}
		byDevice[position.DeviceID] = append(byDevice[position.DeviceID], position)
	}
	for _, deviceID := range order {
		fmt.Fprintf(&builder, "<Placemark><name>%d</name><LineString><coordinates>\n", deviceID)
		for _, position := range byDevice[deviceID] {
			fmt.Fprintf(&builder, "%s,%s,0\n",
				formatNumber(position.Longitude), formatNumber(position.Latitude))
		}
		builder.WriteString("</coordinates></LineString></Placemark>\n")
	}
	builder.WriteString("</Document>\n</kml>\n")
	if _, err := io.WriteString(w, builder.String()); err != nil {
		return fmt.Errorf("report: kml: %w", err)
	}
	return nil
}
