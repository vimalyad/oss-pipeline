package guard

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRoot walks up for the configuration directory rather than counting
// "../.." levels. The engine moved down one level when the API service was
// added, and a literal path would have gone quietly stale.
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
