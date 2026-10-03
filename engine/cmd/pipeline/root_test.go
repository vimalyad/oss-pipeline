package main

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRoot walks up for the configuration directory, the way findRoot does in
// production.
//
// Not a relative path literal. The engine moved from the repository root into
// engine/ when the Spring Boot service was added, and every "../.." in a test
// silently became wrong by one level -- which showed up as the commit guard
// failing to load and six tests reporting things that had nothing to do with
// their names.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "config", "forbidden-trailers.txt")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no config/forbidden-trailers.txt above the working directory")
		}
		dir = parent
	}
}
