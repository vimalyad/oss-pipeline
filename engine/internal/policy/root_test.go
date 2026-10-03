package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRoot walks up for the configuration directory rather than counting
// "../.." levels, which go stale the moment the tree is rearranged.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "config", "policy.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no config/policy.yaml above the working directory")
		}
		dir = parent
	}
}
