package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestCommandQueryDefaults(t *testing.T) {
	query, args := commandQuery(model.CommandFilter{})

	if !strings.Contains(query, "FROM commands") {
		t.Fatalf("query = %s", query)
	}
	if strings.Contains(query, "WHERE") {
		t.Errorf("query has a WHERE clause without filters: %s", query)
	}
	if !strings.HasSuffix(query, "ORDER BY id DESC LIMIT $1 OFFSET $2") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 2 || args[0] != defaultCommandLimit || args[1] != 0 {
		t.Fatalf("args = %v", args)
	}
}

func TestCommandQueryFilters(t *testing.T) {
	query, args := commandQuery(model.CommandFilter{DeviceID: 7, Limit: 5, Offset: 2})

	if !strings.Contains(query, "WHERE device_id = $1") {
		t.Errorf("query = %s", query)
	}
	if !strings.HasSuffix(query, "LIMIT $2 OFFSET $3") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 3 || args[0] != int64(7) || args[1] != 5 || args[2] != 2 {
		t.Fatalf("args = %v", args)
	}
}

// TestCommandQueryOwnership pins the isolation rule: a token without device
// grants must match nothing rather than every command.
func TestCommandQueryOwnership(t *testing.T) {
	query, args := commandQuery(model.CommandFilter{DeviceIDs: []int64{7, 9}})
	if !strings.Contains(query, "device_id IN ($1, $2)") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 4 || args[0] != int64(7) || args[1] != int64(9) {
		t.Fatalf("args = %v", args)
	}

	query, _ = commandQuery(model.CommandFilter{DeviceIDs: []int64{}})
	if !strings.Contains(query, "WHERE false") {
		t.Errorf("empty grant query = %s", query)
	}
}

func TestCommandQueryClamps(t *testing.T) {
	cases := []struct {
		name   string
		filter model.CommandFilter
		want   []any
	}{
		{"limit too large", model.CommandFilter{Limit: 5000}, []any{maxCommandLimit, 0}},
		{"limit negative", model.CommandFilter{Limit: -5}, []any{defaultCommandLimit, 0}},
		{"offset negative", model.CommandFilter{Offset: -3}, []any{defaultCommandLimit, 0}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, args := commandQuery(test.filter)
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

func TestCreateCommandRejectsMissingDevice(t *testing.T) {
	store := &Store{}
	if err := store.CreateCommand(context.Background(), model.NewCommand(0, "engineStop")); !errors.Is(err, ErrMissingCommand) {
		t.Fatalf("err = %v, want %v", err, ErrMissingCommand)
	}
	if err := store.CreateCommand(context.Background(), nil); !errors.Is(err, ErrMissingCommand) {
		t.Fatalf("nil command err = %v, want %v", err, ErrMissingCommand)
	}
}

// TestCommandStoreIntegration covers the four status transitions and the
// ownership filter against a real database. It runs only when
// TRACCAR_POSTGRES_DSN is set.
func TestCommandStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TRACCAR_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRACCAR_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	suffix := time.Now().Format("150405.000000")
	deviceID, err := store.Register("CMD"+suffix, "gt06")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	otherID, err := store.Register("CM2"+suffix, "gt06")
	if err != nil {
		t.Fatalf("register other: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM devices WHERE id = ANY($1)`, []int64{deviceID, otherID})
	})

	// status reads one row back so every assertion sees what the database
	// holds rather than what the caller's struct still remembers.
	status := func(id int64) (string, *int, bool, bool) {
		t.Helper()
		var (
			gotStatus string
			code      sql.NullInt64
			sentAt    sql.NullTime
			doneAt    sql.NullTime
		)
		if err := store.db.QueryRowContext(ctx, `SELECT status, result_code, sent_at, done_at
			FROM commands WHERE id = $1`, id,
		).Scan(&gotStatus, &code, &sentAt, &doneAt); err != nil {
			t.Fatalf("select command %d: %v", id, err)
		}
		var resultCode *int
		if code.Valid {
			value := int(code.Int64)
			resultCode = &value
		}
		return gotStatus, resultCode, sentAt.Valid, doneAt.Valid
	}

	// pending -> sent -> done
	accepted := model.NewCommand(deviceID, "engineStop")
	if err := store.CreateCommand(ctx, accepted); err != nil {
		t.Fatalf("create: %v", err)
	}
	if accepted.ID == 0 || accepted.Status != model.CommandStatusPending {
		t.Fatalf("created command = %+v", accepted)
	}
	if got, code, sent, done := status(accepted.ID); got != model.CommandStatusPending || code != nil || sent || done {
		t.Fatalf("pending row = %q code=%v sent=%v done=%v", got, code, sent, done)
	}

	if err := store.MarkCommandSent(ctx, accepted.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if got, _, sent, done := status(accepted.ID); got != model.CommandStatusSent || !sent || done {
		t.Fatalf("sent row = %q sent=%v done=%v", got, sent, done)
	}

	if err := store.AcknowledgeCommand(ctx, deviceID, 0); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	got, code, _, done := status(accepted.ID)
	if got != model.CommandStatusDone || !done {
		t.Fatalf("done row = %q done=%v", got, done)
	}
	if code == nil || *code != 0 {
		t.Fatalf("result code = %v, want 0", code)
	}

	// pending -> failed, the offline case: no result code at all
	offline := model.NewCommand(deviceID, "engineResume")
	if err := store.CreateCommand(ctx, offline); err != nil {
		t.Fatalf("create offline: %v", err)
	}
	if err := store.MarkCommandFailed(ctx, offline.ID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if got, code, _, _ := status(offline.ID); got != model.CommandStatusFailed || code != nil {
		t.Fatalf("offline row = %q code=%v", got, code)
	}

	// sent -> failed with the code the device reported
	refused := model.NewCommand(deviceID, "rebootDevice")
	if err := store.CreateCommand(ctx, refused); err != nil {
		t.Fatalf("create refused: %v", err)
	}
	if err := store.MarkCommandSent(ctx, refused.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := store.AcknowledgeCommand(ctx, deviceID, 1); err != nil {
		t.Fatalf("acknowledge refusal: %v", err)
	}
	got, code, _, _ = status(refused.ID)
	if got != model.CommandStatusFailed || code == nil || *code != 1 {
		t.Fatalf("refused row = %q code=%v", got, code)
	}

	// An answer closes exactly one row: the newest still waiting.
	first := model.NewCommand(deviceID, "positionSingle")
	second := model.NewCommand(deviceID, "positionStop")
	for _, command := range []*model.Command{first, second} {
		if err := store.CreateCommand(ctx, command); err != nil {
			t.Fatalf("create queued: %v", err)
		}
		if err := store.MarkCommandSent(ctx, command.ID); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
	}
	if err := store.AcknowledgeCommand(ctx, deviceID, 0); err != nil {
		t.Fatalf("acknowledge queued: %v", err)
	}
	if got, _, _, _ := status(first.ID); got != model.CommandStatusSent {
		t.Errorf("older row = %q, want %q", got, model.CommandStatusSent)
	}
	if got, _, _, _ := status(second.ID); got != model.CommandStatusDone {
		t.Errorf("newer row = %q, want %q", got, model.CommandStatusDone)
	}

	// Acknowledging again with nothing waiting is a no-op.
	if err := store.AcknowledgeCommand(ctx, deviceID, 0); err != nil {
		t.Fatalf("spare acknowledge: %v", err)
	}

	foreign := model.NewCommand(otherID, "engineStop")
	if err := store.CreateCommand(ctx, foreign); err != nil {
		t.Fatalf("create foreign: %v", err)
	}

	own, err := store.QueryCommands(ctx, model.CommandFilter{DeviceIDs: []int64{deviceID}})
	if err != nil {
		t.Fatalf("query own: %v", err)
	}
	if len(own) != 4 {
		t.Fatalf("own commands = %d, want 4", len(own))
	}
	for _, command := range own {
		if command.DeviceID != deviceID {
			t.Fatalf("ownership filter leaked device %d", command.DeviceID)
		}
	}
	if own[0].ID != second.ID || own[0].Status != model.CommandStatusDone {
		t.Errorf("newest first = %+v", own[0])
	}

	other, err := store.QueryCommands(ctx, model.CommandFilter{DeviceID: otherID})
	if err != nil {
		t.Fatalf("query other: %v", err)
	}
	if len(other) != 1 || other[0].ID != foreign.ID {
		t.Fatalf("other commands = %+v", other)
	}

	unscoped, err := store.QueryCommands(ctx, model.CommandFilter{DeviceIDs: []int64{}})
	if err != nil {
		t.Fatalf("query unscoped: %v", err)
	}
	if len(unscoped) != 0 {
		t.Fatalf("empty grant returned %d commands", len(unscoped))
	}
}
