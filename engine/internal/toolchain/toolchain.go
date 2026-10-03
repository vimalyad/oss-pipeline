// Package toolchain works out how a repository builds and tests itself.
//
// Two bugs in the previous version shaped this one. It matched the first
// marker file it found and stopped, so a repo that is Go plus a TypeScript UI,
// or Rust plus Python bindings, got half an answer. And it emitted `npm test`
// for any package.json, which is wrong for the pnpm workspaces that several
// watchlist repos use -- the command fails in a way that looks like a broken
// patch rather than a wrong command.
package toolchain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind is an ecosystem present in a repository.
type Kind string

const (
	KindGo     Kind = "go"
	KindCargo  Kind = "cargo"
	KindNode   Kind = "node"
	KindPython Kind = "python"
	KindCMake  Kind = "cmake"
	KindMaven  Kind = "maven"
)

// Toolchain is one ecosystem and how to exercise it.
type Toolchain struct {
	Kind Kind
	// Dir is relative to the repo root; a monorepo can have several.
	Dir     string
	Test    []string
	Lint    []string
	Install []string
	// PackageManager is npm, pnpm or yarn for Node repos.
	PackageManager string
}

// Detect returns every ecosystem in the repo, not just the first.
func Detect(root string) []Toolchain {
	var out []Toolchain
	if exists(root, "go.mod") {
		out = append(out, Toolchain{
			Kind: KindGo, Dir: ".",
			Test: []string{"go test ./..."},
			Lint: []string{"go vet ./..."},
		})
	}
	if exists(root, "Cargo.toml") {
		out = append(out, Toolchain{
			Kind: KindCargo, Dir: ".",
			Test: []string{"cargo test"},
			Lint: []string{"cargo fmt --check", "cargo clippy -- -D warnings"},
		})
	}
	if exists(root, "package.json") {
		out = append(out, nodeToolchain(root))
	}
	if exists(root, "pyproject.toml") || exists(root, "setup.py") || exists(root, "tox.ini") {
		out = append(out, Toolchain{
			Kind: KindPython, Dir: ".",
			// No `uv run` wrapper: the container image installs the
			// environment, so the command is just the runner. That also stops
			// uv.lock being created inside the clone, which the submit guard
			// had to clean up afterwards.
			Test: []string{"python -m pytest -q"},
		})
	}
	if exists(root, "CMakeLists.txt") {
		out = append(out, Toolchain{Kind: KindCMake, Dir: "."})
	}
	if exists(root, "pom.xml") {
		out = append(out, Toolchain{Kind: KindMaven, Dir: ".",
			Test: []string{"mvn -q test"}})
	}
	return out
}

// Primary is the ecosystem most likely to own a change, used when a single
// answer is needed. Order reflects which is usually the subject of a bug
// report in these repos rather than an alphabetical accident.
func Primary(ts []Toolchain) (Toolchain, bool) {
	for _, want := range []Kind{KindGo, KindCargo, KindPython, KindNode, KindCMake, KindMaven} {
		for _, t := range ts {
			if t.Kind == want {
				return t, true
			}
		}
	}
	return Toolchain{}, false
}

// nodeToolchain reads the declared package manager and lockfiles. Running
// `npm test` in a pnpm workspace fails in a way that reads like a broken
// patch, which is exactly the confusion worth avoiding.
func nodeToolchain(root string) Toolchain {
	t := Toolchain{Kind: KindNode, Dir: ".", PackageManager: "npm"}

	var pkg struct {
		PackageManager string            `json:"packageManager"`
		Scripts        map[string]string `json:"scripts"`
		Workspaces     any               `json:"workspaces"`
	}
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		_ = json.Unmarshal(b, &pkg)
	}

	switch {
	case strings.HasPrefix(pkg.PackageManager, "pnpm"):
		t.PackageManager = "pnpm"
	case strings.HasPrefix(pkg.PackageManager, "yarn"):
		t.PackageManager = "yarn"
	case exists(root, "pnpm-lock.yaml"), exists(root, "pnpm-workspace.yaml"):
		t.PackageManager = "pnpm"
	case exists(root, "yarn.lock"):
		t.PackageManager = "yarn"
	}

	switch t.PackageManager {
	case "pnpm":
		t.Install = []string{"pnpm install --frozen-lockfile"}
	case "yarn":
		t.Install = []string{"yarn install --immutable"}
	default:
		t.Install = []string{"npm ci"}
	}
	if _, ok := pkg.Scripts["test"]; ok {
		t.Test = []string{t.PackageManager + " test"}
	}
	for _, s := range []string{"lint", "typecheck"} {
		if _, ok := pkg.Scripts[s]; ok {
			t.Lint = append(t.Lint, t.PackageManager+" run "+s)
		}
	}
	return t
}

// TestFileRe matches files a test runner can actually collect.
//
// Living under tests/ does not make a file a test: one repo keeps every binary
// fixture in test/assets/, and a directory-based filter handed a JPEG and a
// licence file to pytest as test targets.
var TestFileRe = regexp.MustCompile(
	`(^|/)(test_[^/]+\.py|[^/]+_test\.py|conftest\.py` +
		`|[^/]+\.(test|spec)\.(js|jsx|ts|tsx|mjs|cjs)` +
		`|[^/]+_test\.go|[^/]+\.rs)$`)

func IsTestFile(path string) bool { return TestFileRe.MatchString(path) }

// MaxTestTargets bounds how many files are handed to a runner at once.
const MaxTestTargets = 25

// defRe finds symbol definitions in a diff hunk header or changed line.
var defRe = regexp.MustCompile(`(?:def|class|func|fn|export function)\s+([A-Za-z_]\w*)`)

// ChangedSymbols extracts the symbols a diff actually touched.
//
// Reads `git diff -U0` output: git puts the enclosing function on each @@
// header, which is precisely the question being asked. Scanning whole files
// and truncating was wrong twice over -- it returned every symbol in the file,
// and the cut dropped the only one that had changed, so the caller search
// missed the module that actually broke and CI found it instead.
func ChangedSymbols(diff string) []string {
	seen := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			if i := strings.LastIndex(line, "@@"); i >= 0 {
				for _, m := range defRe.FindAllStringSubmatch(line[i+2:], -1) {
					seen[m[1]] = true
				}
			}
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// file headers, not content
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			for _, m := range defRe.FindAllStringSubmatch(line[1:], -1) {
				seen[m[1]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		// Very short names match everything; they cost more than they find.
		if len(s) > 4 {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// envFailureMarkers are outputs meaning the run never got off the ground.
//
// An unbuildable environment is not a failing patch. One repo declares no
// dependencies at all and compiles a C++ extension in CI, so importing it from
// source fails no matter how good the diff is; calling that a bad patch
// discards correct work and blames the wrong thing.
var envFailureMarkers = []string{
	"modulenotfounderror", "importerror while loading conftest",
	"no module named", "command not found", "error while finding module",
	"failed building wheel", "could not build wheels", "no matching distribution",
	// Container-specific, added because the sandbox runs tests without network.
	"network is unreachable", "temporary failure in name resolution",
	"getaddrinfo", "connection refused", "could not resolve host",
	// OOM inside a memory-capped container.
	"signal: killed", "out of memory", "killed (exit code 137)",
}

// envFailureCodes: pytest 2=interrupted, 3=internal, 4=usage, 5=nothing
// collected; 127=command not found; 137=SIGKILL, usually the memory cap.
var envFailureCodes = map[int]bool{2: true, 3: true, 4: true, 5: true, 127: true, 137: true}

// IsEnvironmentFailure reports that a run failed to start rather than failed.
//
// Deliberately narrower than the previous version, which matched the bare
// phrase "does not exist" -- common enough in ordinary assertion messages that
// it would excuse a genuine regression as an environment problem.
func IsEnvironmentFailure(exitCode int, output string) bool {
	if envFailureCodes[exitCode] {
		return true
	}
	low := strings.ToLower(output)
	for _, m := range envFailureMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
