package postgres

import (
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

func TestDeviceIDsClause(t *testing.T) {
	args := make([]any, 0, 2)
	clause := deviceIDsClause(&args, "device_id", []int64{7, 8})

	if clause != "device_id IN ($1, $2)" {
		t.Fatalf("clause = %q", clause)
	}
	if len(args) != 2 || args[0] != int64(7) || args[1] != int64(8) {
		t.Fatalf("args = %#v", args)
	}

	args = args[:0]
	if clause := deviceIDsClause(&args, "device_id", nil); clause != "false" {
		t.Fatalf("empty grant clause = %q, want false", clause)
	}
	if len(args) != 0 {
		t.Fatalf("empty grant args = %#v", args)
	}
}

func TestDeviceQueryScoped(t *testing.T) {
	query, args := deviceQuery(model.DeviceFilter{DeviceIDs: []int64{7}})

	if !strings.Contains(query, "WHERE id IN ($1)") {
		t.Errorf("query = %s", query)
	}
	if !strings.HasSuffix(query, "LIMIT $2 OFFSET $3") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 3 || args[0] != int64(7) {
		t.Fatalf("args = %#v", args)
	}
}

func TestDeviceQueryWithoutDevicesMatchesNothing(t *testing.T) {
	query, _ := deviceQuery(model.DeviceFilter{DeviceIDs: []int64{}})

	if !strings.Contains(query, "WHERE false") {
		t.Errorf("query = %s", query)
	}
}

func TestPositionQueryScoped(t *testing.T) {
	query, args := positionQuery(model.PositionFilter{DeviceIDs: []int64{7, 8}})

	if !strings.Contains(query, "WHERE device_id IN ($1, $2)") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 4 || args[0] != int64(7) || args[1] != int64(8) {
		t.Fatalf("args = %#v", args)
	}
}

func TestAlertQueryScoped(t *testing.T) {
	query, args := eventQuery(model.EventFilter{DeviceIDs: []int64{7}})

	if !strings.Contains(query, "WHERE device_id IN ($1)") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 2 || args[0] != int64(7) {
		t.Fatalf("args = %#v", args)
	}
}
