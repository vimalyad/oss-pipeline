package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, body := range files {
		p := filepath.Join(d, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	return d
}

// A repo that is Go plus a TypeScript UI must report both. Matching the first
// marker file and stopping gave half an answer for several watchlist repos.
func TestDetectFindsEveryEcosystem(t *testing.T) {
	d := repoWith(t, map[string]string{
		"go.mod":       "module x",
		"package.json": `{"scripts":{"test":"vitest"}}`,
		"Cargo.toml":   "[package]",
	})
	got := Detect(d)
	kinds := map[Kind]bool{}
	for _, tc := range got {
		kinds[tc.Kind] = true
	}
	for _, want := range []Kind{KindGo, KindNode, KindCargo} {
		if !kinds[want] {
			t.Errorf("missed %s; got %v", want, kinds)
		}
	}
}

// Running `npm test` in a pnpm workspace fails in a way that reads like a
// broken patch rather than a wrong command.
func TestNodePackageManagerIsDetected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"declared pnpm", map[string]string{
			"package.json": `{"packageManager":"pnpm@9.0.0","scripts":{"test":"vitest"}}`}, "pnpm"},
		{"pnpm lockfile", map[string]string{
			"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": ""}, "pnpm"},
		{"pnpm workspace", map[string]string{
			"package.json": `{"scripts":{"test":"x"}}`, "pnpm-workspace.yaml": ""}, "pnpm"},
		{"yarn lockfile", map[string]string{
			"package.json": `{"scripts":{"test":"x"}}`, "yarn.lock": ""}, "yarn"},
		{"plain npm", map[string]string{
			"package.json": `{"scripts":{"test":"x"}}`}, "npm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := Detect(repoWith(t, tc.files))
			if len(ts) != 1 {
				t.Fatalf("got %d toolchains", len(ts))
			}
			if ts[0].PackageManager != tc.want {
				t.Fatalf("package manager = %q want %q", ts[0].PackageManager, tc.want)
			}
			if len(ts[0].Test) == 0 || !strings.HasPrefix(ts[0].Test[0], tc.want) {
				t.Fatalf("test command = %v, should use %s", ts[0].Test, tc.want)
			}
		})
	}
}

// No test script means no test command, rather than a command that fails.
func TestNodeWithoutTestScript(t *testing.T) {
	ts := Detect(repoWith(t, map[string]string{"package.json": `{"name":"x"}`}))
	if len(ts[0].Test) != 0 {
		t.Fatalf("invented a test command: %v", ts[0].Test)
	}
}

// The Python command no longer wraps in `uv run`, which used to create a
// uv.lock inside the clone that the submit guard then had to clean up.
func TestPythonCommandDoesNotCreateLockfiles(t *testing.T) {
	ts := Detect(repoWith(t, map[string]string{"pyproject.toml": "[project]"}))
	if len(ts) != 1 || ts[0].Kind != KindPython {
		t.Fatalf("got %v", ts)
	}
	if strings.Contains(strings.Join(ts[0].Test, " "), "uv run") {
		t.Fatal("the container provides the environment; uv run litters the clone")
	}
}

func TestIsTestFileMatchesFilesNotDirectories(t *testing.T) {
	for _, p := range []string{
		"tests/test_core.py", "pkg/thing_test.go", "src/x.spec.ts",
		"conftest.py", "src/lib.rs",
	} {
		if !IsTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	// The real regression: binary fixtures under a test directory were being
	// handed to pytest as targets.
	for _, p := range []string{
		"test/assets/damaged_jpeg/TensorFlow-LICENSE",
		"test/assets/x.jpg", "tests/README.md", "testdata/input.json",
	} {
		if IsTestFile(p) {
			t.Errorf("%s is not a runnable test", p)
		}
	}
}

// Scanning whole files returned every symbol and truncation dropped the only
// one that mattered; the hunk header is the actual answer.
func TestChangedSymbolsComeFromTheDiff(t *testing.T) {
	diff := `diff --git a/x.py b/x.py
--- a/x.py
+++ b/x.py
@@ -10,0 +11,2 @@ def _torch_svd_cast(x):
+    if is_mps_tensor_safe(x):
+        return cpu_path(x)
`
	got := ChangedSymbols(diff)
	var found bool
	for _, s := range got {
		if s == "_torch_svd_cast" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missed the enclosing function: %v", got)
	}
}

func TestIsEnvironmentFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		out  string
		want bool
	}{
		{"nothing collected", 5, "", true},
		{"command missing", 127, "go: command not found", true},
		{"import error", 1, "ModuleNotFoundError: No module named 'torch'", true},
		{"no network in container", 1, "Network is unreachable", true},
		{"oom kill", 137, "", true},
		{"real test failure", 1, "FAILED test_x.py::test_y - assert 1 == 2", false},
		{"real build failure", 2, "", true}, // exit 2 is pytest interrupted
		// The previous version matched the bare phrase "does not exist",
		// which appears in ordinary assertion messages and would have excused
		// a genuine regression as an environment problem.
		{"assertion mentioning existence", 1,
			"AssertionError: expected key 'foo' but it does not exist", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEnvironmentFailure(tc.code, tc.out); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// Run against the clones actually on this machine, so the detection is
// checked against real repositories rather than only fixtures.
func TestAgainstRealClones(t *testing.T) {
	wd, _ := os.Getwd()
	work := filepath.Join(filepath.Dir(filepath.Dir(wd)), "work")
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Skip("no clones on this machine")
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(work, e.Name())
		ts := Detect(root)
		var kinds []string
		for _, tc := range ts {
			k := string(tc.Kind)
			if tc.PackageManager != "" {
				k += "/" + tc.PackageManager
			}
			kinds = append(kinds, k)
		}
		p, ok := Primary(ts)
		primary := "none"
		if ok {
			primary = string(p.Kind)
		}
		t.Logf("%-28s %-28v primary=%s", e.Name(), kinds, primary)
	}
}
