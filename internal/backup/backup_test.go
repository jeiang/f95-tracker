package backup_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jeiang/f95-tracker/internal/backup"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func TestRunCopiesAndRefusesExistingDest(t *testing.T) {
	s := testutil.NewStore(t)
	g, _ := testutil.InsertGame(t, s, testutil.GameSpec{Name: "Backed Up"})
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "nested", "backup", "f95-tracker.db")

	if err := backup.Run(ctx, s, dest); err != nil {
		t.Fatal(err)
	}
	copyDB, err := db.Open(ctx, dest)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copyDB.Close()
	got, err := copyDB.Queries().GetGame(ctx, g.ID)
	if err != nil || got.Name != "Backed Up" {
		t.Fatalf("backup content: %+v, %v", got, err)
	}
	if err := backup.Run(ctx, s, dest); err == nil {
		t.Fatal("second backup to the same dest must fail")
	}
}
