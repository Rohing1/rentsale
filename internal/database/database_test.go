package database

import (
	"io/fs"
	"testing"
)

func TestEmbeddedMigrationsArePresent(t *testing.T) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		fatalf(t, "read embedded migrations: %v", err)
	}
	if len(entries) < 3 || entries[0].Name() != "001_init.sql" || entries[1].Name() != "002_harden_inventory_invariants.sql" || entries[2].Name() != "003_add_idempotency.sql" {
		fatalf(t, "unexpected migrations: %#v", entries)
	}
}

func fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}
