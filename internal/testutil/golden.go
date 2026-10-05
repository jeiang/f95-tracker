// Package testutil holds test helpers shared by all packages: golden files,
// a migrated temp-dir Store and a Game builder.
package testutil

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Golden compares got, marshalled as indented JSON, with testdata/<name>.golden.json
// relative to the calling package; run the package's tests with -update to rewrite it.
func Golden(t testing.TB, name string, got any) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("golden %s: marshal: %v", name, err)
	}
	b = append(b, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update to create)", name, err)
	}
	if !bytes.Equal(want, b) {
		t.Errorf("golden %s mismatch (run with -update to accept)\n--- want\n%s\n--- got\n%s", name, want, b)
	}
}
