package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestTemporaryCleanupDeletesExpiredAccounts(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{expiredTemporary: []model.User{
		{ID: 11, Temporary: true},
		{ID: 12, Temporary: true},
	}}
	var reported []string
	task := &TemporaryCleanup{
		Store: store,
		Now:   func() time.Time { return now },
		Report: func(kind string, _ int64) {
			reported = append(reported, kind)
		},
	}

	if err := task.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.deletedUsers) != 2 || store.deletedUsers[0] != 11 || store.deletedUsers[1] != 12 {
		t.Fatalf("deleted = %v", store.deletedUsers)
	}
	if len(reported) != 1 || reported[0] != "temporary accounts" {
		t.Fatalf("reported = %v", reported)
	}
}

func TestTemporaryCleanupSkipsAnAccountAnAdminAlreadyRemoved(t *testing.T) {
	store := &fakeStore{
		expiredTemporary: []model.User{{ID: 11}, {ID: 12}},
		deleteErr:        map[int64]error{11: model.ErrUserNotFound},
	}

	if err := (&TemporaryCleanup{Store: store}).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.deletedUsers) != 1 || store.deletedUsers[0] != 12 {
		t.Fatalf("deleted = %v", store.deletedUsers)
	}
}

func TestTemporaryCleanupStopsOnADeleteFailure(t *testing.T) {
	store := &fakeStore{
		expiredTemporary: []model.User{{ID: 11}, {ID: 12}},
		deleteErr:        map[int64]error{11: errors.New("boom")},
	}

	if err := (&TemporaryCleanup{Store: store}).Run(context.Background()); err == nil {
		t.Fatal("the delete failure was swallowed")
	}
	if len(store.deletedUsers) != 0 {
		t.Fatalf("deleted = %v, want the pass to stop", store.deletedUsers)
	}
}

func TestTemporaryCleanupReportsAStoreFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("boom")}
	if err := (&TemporaryCleanup{Store: store}).Run(context.Background()); err == nil {
		t.Fatal("the store failure was swallowed")
	}
}

func TestTemporaryCleanupWithoutStoreDoesNothing(t *testing.T) {
	if err := (&TemporaryCleanup{}).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}
