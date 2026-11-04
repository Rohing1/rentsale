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
	expected := []string{
		"001_init.sql",
		"002_harden_inventory_invariants.sql",
		"003_add_idempotency.sql",
		"004_make_payment_reference_global.sql",
		"005_add_payment_report_index.sql",
	}
	if len(entries) < len(expected) {
		fatalf(t, "unexpected migrations: %#v", entries)
	}
	for index, name := range expected {
		if entries[index].Name() != name {
			fatalf(t, "migration %d = %q, want %q", index, entries[index].Name(), name)
		}
	}
}

func fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}
