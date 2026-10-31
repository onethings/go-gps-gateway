package postgres

import (
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
