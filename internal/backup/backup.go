// Package backup implements `f95-tracker backup <dest>`: an online VACUUM INTO.
package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeiang/f95-tracker/internal/db"
)

// Run writes a consistent copy of the database to dest, creating the parent
// directory. It fails if dest already exists.
func Run(ctx context.Context, store *db.Store, dest string) error {
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("backup: %s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := store.VacuumInto(ctx, dest); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return os.Chmod(dest, 0o600)
}
