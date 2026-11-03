package storage

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestDeviceQueryDefaults(t *testing.T) {
	query, args := deviceQuery(model.DeviceFilter{})

	if !strings.Contains(query, "FROM devices") {
		t.Fatalf("query = %s", query)
	}
	if strings.Contains(query, "WHERE") {
		t.Errorf("query has a WHERE clause without filters: %s", query)
	}
	if !strings.Contains(query, "ORDER BY (last_update IS NULL), last_update DESC, id DESC") {
		t.Errorf("query = %s", query)
	}
	if !strings.HasSuffix(query, "LIMIT $1 OFFSET $2") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 2 || args[0] != defaultDeviceLimit || args[1] != 0 {
		t.Fatalf("args = %v", args)
	}
}

func TestDeviceQueryFilters(t *testing.T) {
	query, args := deviceQuery(model.DeviceFilter{Protocol: "gt06", Limit: 5, Offset: 2})

	if !strings.Contains(query, "WHERE protocol = $1") {
		t.Errorf("query = %s", query)
	}
	if !strings.HasSuffix(query, "LIMIT $2 OFFSET $3") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 3 || args[0] != "gt06" || args[1] != 5 || args[2] != 2 {
		t.Fatalf("args = %v", args)
	}
}

func TestDeviceQueryClamps(t *testing.T) {
	cases := []struct {
		name   string
		filter model.DeviceFilter
		want   []any
	}{
		{"limit too large", model.DeviceFilter{Limit: 5000}, []any{maxDeviceLimit, 0}},
		{"limit negative", model.DeviceFilter{Limit: -5}, []any{defaultDeviceLimit, 0}},
		{"offset negative", model.DeviceFilter{Offset: -3}, []any{defaultDeviceLimit, 0}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, args := deviceQuery(test.filter)
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

func TestDeviceTimeOf(t *testing.T) {
	position := model.NewPosition("gt06")
	if got := deviceTimeOf(position); got.IsZero() {
		t.Fatal("zero device time was not defaulted")
	}

	position.Time = time.Date(2026, 10, 31, 13, 2, 10, 0, time.FixedZone("CST", 8*3600))
	got := deviceTimeOf(position)
	if got.Format(time.RFC3339) != "2026-10-31T05:02:10Z" {
		t.Fatalf("device time = %s, want UTC", got)
	}
}

func TestDeviceQueryLookups(t *testing.T) {
	query, args := deviceQuery(model.DeviceFilter{
		UniqueIDs: []string{"A", "B"},
		IDs:       []int64{7},
	})

	if !strings.Contains(query, "(unique_id IN ($1, $2) OR id IN ($3))") {
		t.Errorf("query = %s", query)
	}
	if !strings.Contains(query, "LIMIT $4 OFFSET $5") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 5 || args[0] != "A" || args[1] != "B" || args[2] != int64(7) {
		t.Fatalf("args = %v", args)
	}

	// The Java lookup branch ignores the keyword, so the clause is skipped.
	withKeyword, _ := deviceQuery(model.DeviceFilter{UniqueIDs: []string{"A"}, Keyword: "van"})
	if strings.Contains(withKeyword, "LIKE") {
		t.Errorf("query = %s, want no keyword clause next to a uniqueId lookup", withKeyword)
	}
}

func TestDeviceQueryKeyword(t *testing.T) {
	query, args := deviceQuery(model.DeviceFilter{Keyword: "van"})

	if !strings.Contains(query, "LOWER(name) LIKE LOWER($1)") ||
		!strings.Contains(query, "LOWER(unique_id) LIKE LOWER($1)") ||
		!strings.Contains(query, "LOWER(COALESCE(model, '')) LIKE LOWER($1)") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 3 || args[0] != "%van%" {
		t.Fatalf("args = %v", args)
	}
}

// fakeRow fills the destinations scanDevice asks for, so a column added to
// deviceColumns without a matching scan target fails here instead of at runtime.
type deviceRow struct {
	values   []any
	assigned int
}

func (r *deviceRow) Scan(dest ...any) error {
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
		case *sql.NullString:
			*pointer = r.values[index].(sql.NullString)
		case *sql.NullInt64:
			*pointer = r.values[index].(sql.NullInt64)
		case *sql.NullTime:
			*pointer = r.values[index].(sql.NullTime)
		default:
			panic("unsupported scan target")
		}
	}
	return nil
}

func TestScanDeviceReadsEveryColumn(t *testing.T) {
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	row := &deviceRow{values: []any{
		int64(7), "864717003283581", "van-7",
		sql.NullString{String: "GT06N", Valid: true},
		sql.NullString{String: "gt06", Valid: true},
		sql.NullInt64{Int64: 4, Valid: true},
		[]byte(`{"floor":10}`), "online",
		sql.NullTime{Time: created, Valid: true}, created,
		sql.NullString{String: "+886912345678", Valid: true},
		sql.NullString{String: "ops", Valid: true},
		sql.NullString{String: "van", Valid: true},
		true,
		sql.NullTime{Time: expires, Valid: true},
		int64(3), int64(11),
	}}

	device, err := scanDevice(row)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if row.assigned != 17 {
		t.Fatalf("scan targets = %d, want 17", row.assigned)
	}
	if device.Phone != "+886912345678" || device.Contact != "ops" || device.Category != "van" {
		t.Errorf("device = %+v", device)
	}
	if !device.Disabled || device.CalendarID != 3 || device.PositionID != 11 {
		t.Errorf("device = %+v", device)
	}
	if !device.ExpirationTime.Equal(expires) || !device.LastUpdate.Equal(created) {
		t.Errorf("device = %+v", device)
	}
	if device.Attributes["floor"] != float64(10) {
		t.Errorf("attributes = %v", device.Attributes)
	}
}
