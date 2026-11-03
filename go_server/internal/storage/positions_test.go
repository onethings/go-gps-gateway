package postgres

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestPositionQueryDefaults(t *testing.T) {
	query, args := positionQuery(model.PositionFilter{})

	if !strings.Contains(query, "FROM positions") {
		t.Fatalf("query = %s", query)
	}
	if strings.Contains(query, "WHERE") {
		t.Errorf("query has a WHERE clause without filters: %s", query)
	}
	if !strings.Contains(query, "ORDER BY device_time DESC, id DESC") {
		t.Errorf("query = %s", query)
	}
	if !strings.HasSuffix(query, "LIMIT $1 OFFSET $2") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v", args)
	}
	if args[0] != defaultPositionLimit || args[1] != 0 {
		t.Errorf("limit/offset args = %v", args)
	}
}

func TestPositionQueryFilters(t *testing.T) {
	from := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	query, args := positionQuery(model.PositionFilter{
		DeviceID: 7,
		From:     from,
		To:       to,
		Limit:    50,
		Offset:   10,
	})

	for _, clause := range []string{
		"device_id = $1",
		"device_time >= $2",
		"device_time <= $3",
		"LIMIT $4 OFFSET $5",
	} {
		if !strings.Contains(query, clause) {
			t.Errorf("query is missing %q: %s", clause, query)
		}
	}

	if len(args) != 5 {
		t.Fatalf("args = %v", args)
	}
	if args[0] != int64(7) || args[1] != from || args[2] != to {
		t.Errorf("args = %v", args)
	}
	if args[3] != 50 || args[4] != 10 {
		t.Errorf("limit/offset args = %v", args[3:])
	}
}

func TestPositionQueryClamps(t *testing.T) {
	cases := []struct {
		name   string
		filter model.PositionFilter
		want   []any
	}{
		{"limit too large", model.PositionFilter{Limit: 5000}, []any{maxPositionLimit, 0}},
		{"limit negative", model.PositionFilter{Limit: -5}, []any{defaultPositionLimit, 0}},
		{"offset negative", model.PositionFilter{Offset: -3}, []any{defaultPositionLimit, 0}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, args := positionQuery(test.filter)
			if len(args) != len(test.want) {
				t.Fatalf("args = %v", args)
			}
			for i := range test.want {
				if args[i] != test.want[i] {
					t.Fatalf("args = %v, want %v", args, test.want)
				}
			}
		})
	}
}

func TestPositionQueryAscending(t *testing.T) {
	query, _ := positionQuery(model.PositionFilter{Ascending: true})
	if !strings.Contains(query, "ORDER BY device_time ASC, id ASC") {
		t.Errorf("query = %s, want the ascending track order", query)
	}

	descending, _ := positionQuery(model.PositionFilter{})
	if !strings.Contains(descending, "ORDER BY device_time DESC, id DESC") {
		t.Errorf("query = %s", descending)
	}
}

func TestLatestPositionsQuery(t *testing.T) {
	query, args := latestPositionsQuery(nil)
	if !strings.Contains(query, "SELECT DISTINCT ON (device_id)") {
		t.Errorf("query = %s", query)
	}
	if strings.Contains(query, "WHERE") {
		t.Errorf("query has a WHERE clause without devices: %s", query)
	}
	if !strings.HasSuffix(query, "ORDER BY device_id, device_time DESC, id DESC") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 0 {
		t.Errorf("args = %v", args)
	}

	query, args = latestPositionsQuery([]int64{7, 8})
	if !strings.Contains(query, "WHERE device_id IN ($1, $2)") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 2 || args[0] != int64(7) || args[1] != int64(8) {
		t.Errorf("args = %v", args)
	}

	query, _ = latestPositionsQuery([]int64{})
	if !strings.Contains(query, "WHERE false") {
		t.Errorf("query = %s, want an empty scope to match nothing", query)
	}
}

// TestPositionQueryIDs covers the id list GET /api/positions/{id} uses.
func TestPositionQueryIDs(t *testing.T) {
	query, args := positionQuery(model.PositionFilter{IDs: []int64{11}})

	if !strings.Contains(query, "id IN ($1)") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 3 || args[0] != int64(11) {
		t.Fatalf("args = %v", args)
	}

	query, args = positionQuery(model.PositionFilter{IDs: []int64{}})
	if !strings.Contains(query, "WHERE false") {
		t.Errorf("an empty id list must match nothing: %s", query)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v", args)
	}
}

// positionRow fills the destinations scanPosition asks for.
type positionRow struct {
	values   []any
	assigned int
}

func (r *positionRow) Scan(dest ...any) error {
	r.assigned = len(dest)
	for index, target := range dest {
		if index >= len(r.values) {
			break
		}
		switch pointer := target.(type) {
		case *int64:
			*pointer = r.values[index].(int64)
		case *string:
			*pointer = r.values[index].(string)
		case *bool:
			*pointer = r.values[index].(bool)
		case *time.Time:
			*pointer = r.values[index].(time.Time)
		case *[]byte:
			*pointer = r.values[index].([]byte)
		case *sql.NullFloat64:
			*pointer = r.values[index].(sql.NullFloat64)
		default:
			panic("unsupported scan target")
		}
	}
	return nil
}

func TestScanPositionReadsTheGeofenceIDs(t *testing.T) {
	at := time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)

	row := &positionRow{values: []any{
		int64(11), int64(7), "gt06", at, at, true,
		sql.NullFloat64{Float64: 54.943, Valid: true},
		sql.NullFloat64{Float64: 25.206, Valid: true},
		sql.NullFloat64{}, sql.NullFloat64{}, sql.NullFloat64{}, sql.NullFloat64{},
		[]byte(`{"sat":9}`), []byte(`[5,9]`),
	}}

	position, err := scanPosition(row)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if row.assigned != 14 {
		t.Fatalf("scan targets = %d, want 14", row.assigned)
	}
	if len(position.GeofenceIDs) != 2 || position.GeofenceIDs[1] != 9 {
		t.Errorf("geofence ids = %v", position.GeofenceIDs)
	}
	if position.Latitude != 54.943 || position.Attributes["sat"] != float64(9) {
		t.Errorf("position = %+v", position)
	}
}
