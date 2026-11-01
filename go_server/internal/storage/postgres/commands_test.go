package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
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

// TestAcknowledgeQuery pins how an answer picks its row: a serial narrows the
// statement to the row the device named, and no serial falls back to the newest
// row that is still waiting.
func TestAcknowledgeQuery(t *testing.T) {
	serial := 11
	query, args := acknowledgeQuery(7, codec.CommandAck{Code: 0, Serial: &serial})
	if !strings.Contains(query, "device_id = $1 AND serial = $2") {
		t.Errorf("query = %s", query)
	}
	if !strings.Contains(query, "SET status = $3, result_code = $4") {
		t.Errorf("query = %s", query)
	}
	want := []any{int64(7), 11, model.CommandStatusDone, 0, model.CommandStatusSent}
	if len(args) != len(want) {
		t.Fatalf("args = %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}

	query, args = acknowledgeQuery(7, codec.CommandAck{Code: 1})
	if strings.Contains(query, "serial") {
		t.Errorf("query without a serial filters on it: %s", query)
	}
	if !strings.Contains(query, "SET status = $2, result_code = $3") {
		t.Errorf("query = %s", query)
	}
	want = []any{int64(7), model.CommandStatusFailed, 1, model.CommandStatusSent}
	if len(args) != len(want) || args[1] != model.CommandStatusFailed || args[2] != 1 {
		t.Fatalf("args = %v, want %v", args, want)
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

// commandRow is one commands row as the database holds it.
type commandRow struct {
	status   string
	code     *int
	serial   *int
	attempts int
	sent     bool
	done     bool
}

// TestCommandStoreIntegration covers the four status transitions, the serial
// pairing, the reconnect query, retention and the ownership filter against a
// real database. It runs only when TRACCAR_POSTGRES_DSN is set.
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

	// row reads one command back so every assertion sees what the database
	// holds rather than what the caller's struct still remembers.
	row := func(id int64) commandRow {
		t.Helper()
		var (
			read   commandRow
			code   sql.NullInt64
			serial sql.NullInt64
			sentAt sql.NullTime
			doneAt sql.NullTime
		)
		if err := store.db.QueryRowContext(ctx, `SELECT status, result_code, serial, attempts,
			sent_at, done_at FROM commands WHERE id = $1`, id,
		).Scan(&read.status, &code, &serial, &read.attempts, &sentAt, &doneAt); err != nil {
			t.Fatalf("select command %d: %v", id, err)
		}
		if code.Valid {
			value := int(code.Int64)
			read.code = &value
		}
		if serial.Valid {
			value := int(serial.Int64)
			read.serial = &value
		}
		read.sent = sentAt.Valid
		read.done = doneAt.Valid
		return read
	}

	// pending -> sent -> sent again -> done. The second write is a
	// retransmission: it counts, but sent_at keeps the first time the device
	// saw the command.
	accepted := model.NewCommand(deviceID, "engineStop")
	if err := store.CreateCommand(ctx, accepted); err != nil {
		t.Fatalf("create: %v", err)
	}
	if accepted.ID == 0 || accepted.Status != model.CommandStatusPending {
		t.Fatalf("created command = %+v", accepted)
	}
	if read := row(accepted.ID); read.status != model.CommandStatusPending ||
		read.code != nil || read.sent || read.done || read.attempts != 0 {
		t.Fatalf("pending row = %+v", read)
	}

	if err := store.MarkCommandSent(ctx, accepted.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	firstSend := row(accepted.ID)
	if firstSend.status != model.CommandStatusSent || !firstSend.sent || firstSend.done ||
		firstSend.attempts != 1 {
		t.Fatalf("sent row = %+v", firstSend)
	}

	firstSentAt := time.Now()
	if err := store.MarkCommandSent(ctx, accepted.ID); err != nil {
		t.Fatalf("mark sent again: %v", err)
	}
	if read := row(accepted.ID); read.status != model.CommandStatusSent || read.attempts != 2 {
		t.Fatalf("retransmitted row = %+v", read)
	}
	var sentAt time.Time
	if err := store.db.QueryRowContext(ctx,
		`SELECT sent_at FROM commands WHERE id = $1`, accepted.ID).Scan(&sentAt); err != nil {
		t.Fatalf("select sent_at: %v", err)
	}
	if sentAt.After(firstSentAt) {
		t.Errorf("sent_at moved on a retransmission: %s", sentAt)
	}

	if err := store.AcknowledgeCommand(ctx, deviceID, codec.CommandAck{Code: 0}); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	doneRow := row(accepted.ID)
	if doneRow.status != model.CommandStatusDone || !doneRow.done {
		t.Fatalf("done row = %+v", doneRow)
	}
	if doneRow.code == nil || *doneRow.code != 0 {
		t.Fatalf("result code = %v, want 0", doneRow.code)
	}

	// pending -> failed, the offline case: no result code at all
	offline := model.NewCommand(deviceID, "engineResume")
	if err := store.CreateCommand(ctx, offline); err != nil {
		t.Fatalf("create offline: %v", err)
	}
	if err := store.MarkCommandFailed(ctx, offline.ID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if read := row(offline.ID); read.status != model.CommandStatusFailed || read.code != nil {
		t.Fatalf("offline row = %+v", read)
	}

	// sent -> failed with the code the device reported
	refused := model.NewCommand(deviceID, "rebootDevice")
	if err := store.CreateCommand(ctx, refused); err != nil {
		t.Fatalf("create refused: %v", err)
	}
	if err := store.MarkCommandSent(ctx, refused.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := store.AcknowledgeCommand(ctx, deviceID, codec.CommandAck{Code: 1}); err != nil {
		t.Fatalf("acknowledge refusal: %v", err)
	}
	if read := row(refused.ID); read.status != model.CommandStatusFailed ||
		read.code == nil || *read.code != 1 {
		t.Fatalf("refused row = %+v", read)
	}

	// A serial pairs the answer with the row it names, not with the newest one:
	// the older command is answered first and the newer one keeps waiting.
	older := model.NewCommand(deviceID, "positionSingle")
	older.Serial = 11
	newer := model.NewCommand(deviceID, "positionStop")
	newer.Serial = 12
	for _, command := range []*model.Command{older, newer} {
		if err := store.CreateCommand(ctx, command); err != nil {
			t.Fatalf("create queued: %v", err)
		}
		if err := store.MarkCommandSent(ctx, command.ID); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
	}
	if read := row(older.ID); read.serial == nil || *read.serial != 11 {
		t.Fatalf("serial was not stored: %+v", read)
	}

	serial := 11
	if err := store.AcknowledgeCommand(ctx, deviceID, codec.CommandAck{Serial: &serial}); err != nil {
		t.Fatalf("acknowledge by serial: %v", err)
	}
	if read := row(older.ID); read.status != model.CommandStatusDone {
		t.Errorf("answered row = %+v, want done", read)
	}
	if read := row(newer.ID); read.status != model.CommandStatusSent {
		t.Errorf("newer row = %+v, want sent", read)
	}

	// Without a serial the newest waiting row is the one an answer closes.
	if err := store.AcknowledgeCommand(ctx, deviceID, codec.CommandAck{Code: 0}); err != nil {
		t.Fatalf("acknowledge newest: %v", err)
	}
	if read := row(newer.ID); read.status != model.CommandStatusDone {
		t.Errorf("newest row = %+v, want done", read)
	}

	// Acknowledging again with nothing waiting is a no-op.
	if err := store.AcknowledgeCommand(ctx, deviceID, codec.CommandAck{Code: 0}); err != nil {
		t.Fatalf("spare acknowledge: %v", err)
	}

	// The reconnect path sees exactly the rows that never reached the socket,
	// oldest first, and never a row that is waiting for an answer.
	queuedOne := model.NewCommand(deviceID, "alarmArm")
	queuedTwo := model.NewCommand(deviceID, "alarmDisarm")
	for _, command := range []*model.Command{queuedOne, queuedTwo} {
		if err := store.CreateCommand(ctx, command); err != nil {
			t.Fatalf("create pending: %v", err)
		}
	}
	pending, err := store.PendingCommands(ctx, deviceID, 10)
	if err != nil {
		t.Fatalf("pending commands: %v", err)
	}
	if len(pending) != 2 || pending[0].ID != queuedOne.ID || pending[1].ID != queuedTwo.ID {
		t.Fatalf("pending = %+v", pending)
	}

	// Retention measures a finished command by when it ended and an unfinished
	// one by when it was recorded.
	expired := model.NewCommand(deviceID, "factoryReset")
	if err := store.CreateCommand(ctx, expired); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if err := store.MarkCommandFailed(ctx, expired.ID); err != nil {
		t.Fatalf("mark expired failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE commands SET done_at = now() - interval '30 days' WHERE id = $1`,
		expired.ID); err != nil {
		t.Fatalf("age command: %v", err)
	}
	if _, err := store.DeleteCommandsBefore(ctx, 0); !errors.Is(err, ErrInvalidRetention) {
		t.Fatalf("delete with a zero age = %v, want ErrInvalidRetention", err)
	}
	deleted, err := store.DeleteCommandsBefore(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("delete old commands: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("deleted = %d, want at least the aged command", deleted)
	}
	if _, err := store.db.ExecContext(ctx,
		`SELECT 1 FROM commands WHERE id = $1`, expired.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("aged command survived retention: %v", err)
	}
	if read := row(queuedOne.ID); read.status != model.CommandStatusPending {
		t.Fatalf("retention deleted a command that still waits: %+v", read)
	}

	foreign := model.NewCommand(otherID, "engineStop")
	if err := store.CreateCommand(ctx, foreign); err != nil {
		t.Fatalf("create foreign: %v", err)
	}

	own, err := store.QueryCommands(ctx, model.CommandFilter{DeviceIDs: []int64{deviceID}})
	if err != nil {
		t.Fatalf("query own: %v", err)
	}
	if len(own) != 7 {
		t.Fatalf("own commands = %d, want 7", len(own))
	}
	for _, command := range own {
		if command.DeviceID != deviceID {
			t.Fatalf("ownership filter leaked device %d", command.DeviceID)
		}
	}
	if own[0].ID != queuedTwo.ID || own[0].Status != model.CommandStatusPending {
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
