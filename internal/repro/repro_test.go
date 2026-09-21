package repro

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/recipe"
	"github.com/vimalyad/osspipeline/internal/sandbox"
)

// fakeSession answers with canned results, keyed by a substring of the
// command, and records what it was asked to run.
type fakeSession struct {
	replies map[string]sandbox.Result
	ran     []string
	err     error
}

func (f *fakeSession) Run(_ context.Context, cmd string) (sandbox.Result, error) {
	f.ran = append(f.ran, cmd)
	if f.err != nil {
		return sandbox.Result{}, f.err
	}
	for k, v := range f.replies {
		if strings.Contains(cmd, k) {
			v.Command = cmd
			return v, nil
		}
	}
	return sandbox.Result{Code: 0, Command: cmd}, nil
}

var _ runner = (*fakeSession)(nil)

// TestEnvironmentFailuresAreNotDefects is the point of this package. Every one
// of these produces a red result that looks like a failing test, and treating
// any of them as a reproduction means patching code that was never broken.
func TestEnvironmentFailuresAreNotDefects(t *testing.T) {
	tests := []struct {
		name, output string
		want         Outcome
	}{
		{"dns off", "socket.gaierror: [Errno -3] Temporary failure in name resolution", Environment},
		{"connection refused", "requests.exceptions.ConnectionError: Connection refused", Environment},
		{"max retries", "urllib3: Max retries exceeded with url: /api", Environment},
		{"missing binary", "bash: line 1: ffmpeg: command not found", Environment},
		{"missing shared lib", "ImportError: libGL.so.1: cannot open shared object file", Environment},
		{"oom", "\n/bin/sh: line 2: 41 Killed\n", Environment},
		{"disk", "OSError: [Errno 28] No space left on device", Environment},
		{"permissions", "PermissionError: [Errno 13] Permission denied: '/usr/local/lib'", Environment},
		{"wrong arch", "qemu: uncaught target signal 4 (Illegal instruction)", Environment},
		{"pip cannot resolve", "ERROR: Could not find a version that satisfies the requirement torch", Environment},

		// These are the failures we are actually looking for.
		{"assertion", "E   assert 3 == 4\nFAILED tests/test_x.py::test_y", Reproduced},
		{"compile error", "./main.go:12:6: undefined: Foo", Reproduced},
		{"panic", "panic: runtime error: index out of range [3]", Reproduced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, why := classify(sandbox.Result{Code: 1, Output: tt.output})
			if got != tt.want {
				t.Fatalf("classify = %s (%s), want %s", got, why, tt.want)
			}
			if got == Environment && why == "" {
				t.Error("an environment verdict must say which problem it is")
			}
		})
	}
}

func TestTimeoutIsAnEnvironmentProblem(t *testing.T) {
	// A suite that ran out of time has not told us whether the code is
	// correct, and a timeout is as likely to be our CPU budget as their bug.
	got, why := classify(sandbox.Result{Code: -1, TimedOut: true, Output: "..."})
	if got != Environment || why == "" {
		t.Fatalf("= %s (%q)", got, why)
	}
}

func TestPassingRunIsNeverAReproduction(t *testing.T) {
	if got, _ := classify(sandbox.Result{Code: 0, Output: "ok"}); got != Passed {
		t.Fatalf("= %s", got)
	}
}

// TestInstallFailureIsAlwaysEnvironmental: a repository's dependencies failing
// to install says nothing about the patch, and reporting it as a failing test
// would send us to fix code that is fine.
func TestInstallFailureIsAlwaysEnvironmental(t *testing.T) {
	r := recipe.Recipe{
		Install: []recipe.Step{{Run: "pip install -e ."}, {Run: "pip install pytest"}},
		Test:    []recipe.Step{{Run: "pytest"}},
	}
	f := &fakeSession{replies: map[string]sandbox.Result{
		// A plain non-zero exit with no recognisable pattern: even this must
		// not become a defect when it happens during install.
		"pip install -e .": {Code: 1, Output: "error: subprocess-exited-with-error"},
	}}
	got, err := Install(context.Background(), f, r)
	if !errors.Is(err, ErrEnvironment) {
		t.Fatalf("err = %v, want ErrEnvironment", err)
	}
	if len(got) != 1 {
		t.Fatalf("ran %d steps, want 1: install must stop at the first failure", len(got))
	}
	if len(f.ran) != 1 {
		t.Errorf("kept going after a failed install: %v", f.ran)
	}
}

func TestVerifyRunsEveryTestCommand(t *testing.T) {
	r := recipe.Recipe{Test: []recipe.Step{{Run: "pytest tests/a"}, {Run: "pytest tests/b"}}}
	f := &fakeSession{replies: map[string]sandbox.Result{
		"tests/a": {Code: 1, Output: "FAILED tests/a::test_one"},
	}}
	got, err := Verify(context.Background(), f, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ran %d commands, want 2: one failure must not hide the rest", len(got))
	}
	if Green(got) {
		t.Error("Green with a failing command")
	}
	if n := len(Failed(got)); n != 1 {
		t.Errorf("Failed = %d, want 1", n)
	}
}

func TestVerifyWithoutATestCommandIsAnError(t *testing.T) {
	_, err := Verify(context.Background(), &fakeSession{}, recipe.Recipe{})
	if !errors.Is(err, ErrRepro) {
		t.Fatalf("err = %v, want ErrRepro", err)
	}
}

// TestConfirmSaysSoWhenItCannotReproduce: a failure we cannot reproduce is one
// we cannot claim to have fixed, and the reason has to reach the human.
func TestConfirmSaysSoWhenItCannotReproduce(t *testing.T) {
	f := &fakeSession{replies: map[string]sandbox.Result{"pytest": {Code: 0, Output: "1 passed"}}}
	got, err := Confirm(context.Background(), f, "pytest tests/test_mps.py")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != Passed {
		t.Fatalf("outcome = %s", got.Outcome)
	}
	if !strings.Contains(got.Why, "their runner") {
		t.Errorf("why = %q, want an explanation a human can act on", got.Why)
	}
}

func TestAttempts(t *testing.T) {
	// Three when we can watch it break and pass again; one when each attempt
	// is a guess published to someone else's CI.
	if got := Attempts(true); got != 3 {
		t.Errorf("reproduced: %d, want 3", got)
	}
	if got := Attempts(false); got != 1 {
		t.Errorf("not reproduced: %d, want 1", got)
	}
}

func TestGreenIsFalseForNoResults(t *testing.T) {
	// Zero commands passing is not a green run; it is a run that did not
	// happen, and treating it as success would let an empty recipe ship.
	if Green(nil) {
		t.Fatal("Green(nil) = true")
	}
}

func TestEnvironmentProblemsAreReportedSeparately(t *testing.T) {
	rs := []Result{
		{Outcome: Reproduced, Command: "pytest a"},
		{Outcome: Environment, Command: "pytest b", Why: "no network"},
		{Outcome: Passed, Command: "pytest c"},
	}
	if n := len(Failed(rs)); n != 1 {
		t.Errorf("Failed = %d", n)
	}
	if n := len(EnvironmentProblems(rs)); n != 1 {
		t.Errorf("EnvironmentProblems = %d", n)
	}
}

func TestResultStringIsOneLine(t *testing.T) {
	r := Result{Outcome: Reproduced, Command: "set -e\npytest tests/", Why: "exit 1"}
	if s := r.String(); strings.Count(s, "\n") != 0 {
		t.Errorf("String() spans lines: %q", s)
	}
}

func TestSandboxResultOKContract(t *testing.T) {
	// classify leans on Result.OK; pin the contract it depends on.
	if !(sandbox.Result{Code: 0}).OK() {
		t.Error("exit 0 is not OK")
	}
	if (sandbox.Result{Code: 0, TimedOut: true, Duration: time.Second}).OK() {
		t.Error("a timed-out command reported OK")
	}
}

// TestATestVerdictOutranksEnvironmentPatterns is the correction a real run
// forced. pytest emitted a cache-permission warning alongside a genuine
// assertion failure; scanning the whole output for environment patterns first
// let the warning win, and the real failure went unreported.
func TestATestVerdictOutranksEnvironmentPatterns(t *testing.T) {
	out := `warnings summary
  PytestCacheWarning: could not create cache path /work/.pytest_cache/v: [Errno 13] Permission denied
=========================== short test summary info ============================
FAILED tests/geometry/test_homography.py::TestFindHomographyDLTIter::test_clean_points - AssertionError
`
	got, why := classify(sandbox.Result{Code: 1, Output: out})
	if got != Reproduced {
		t.Fatalf("classify = %s (%s); a suite that printed a failure summary ran", got, why)
	}
}

func TestEnvironmentStillWinsWhenTheSuiteNeverRan(t *testing.T) {
	out := "ImportError: libGL.so.1: cannot open shared object file: No such file or directory\n"
	if got, _ := classify(sandbox.Result{Code: 1, Output: out}); got != Environment {
		t.Fatalf("classify = %s", got)
	}
}

func TestFailingTests(t *testing.T) {
	tests := []struct {
		name, out string
		want      []string
	}{
		{"pytest", "FAILED tests/a.py::test_x - AssertionError\nFAILED tests/b.py::test_y\n",
			[]string{"tests/a.py::test_x", "tests/b.py::test_y"}},
		{"go", "--- FAIL: TestAlpha (0.01s)\n    --- FAIL: TestBeta/sub (0.00s)\n",
			[]string{"TestAlpha", "TestBeta/sub"}},
		{"cargo", "test geom::tests::rotates ... FAILED\n", []string{"geom::tests::rotates"}},
		{"none", "everything passed\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FailingTests(tt.out)
			if len(got) != len(tt.want) {
				t.Fatalf("= %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestNewFailuresIgnoresWhatWasAlreadyRed is what makes an imperfect container
// usable. On linux/arm64 a plain `pip install torch` resolves to a CUDA build
// whose CPU linear algebra returns NaN, so kornia's homography tests fail on
// an unmodified checkout. Without a baseline the loop would set about fixing
// code that was already correct.
func TestNewFailuresIgnoresWhatWasAlreadyRed(t *testing.T) {
	before := []Result{{Outcome: Reproduced, Output: "FAILED tests/geometry/test_homography.py::test_clean_points - nan\n"}}
	after := []Result{{Outcome: Reproduced, Output: "FAILED tests/geometry/test_homography.py::test_clean_points - nan\nFAILED tests/core/test_new.py::test_regression\n"}}

	got := NewFailures(before, after)
	if len(got) != 1 || got[0] != "tests/core/test_new.py::test_regression" {
		t.Fatalf("NewFailures = %v, want only the new one", got)
	}
	if pre := PreexistingFailures(before); len(pre) != 1 {
		t.Errorf("PreexistingFailures = %v", pre)
	}
	// A change that fixes nothing and breaks nothing must produce no news.
	if got := NewFailures(before, before); len(got) != 0 {
		t.Errorf("NewFailures against itself = %v", got)
	}
}
