package cilog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	wd, _ := os.Getwd()
	p := filepath.Join(filepath.Dir(filepath.Dir(wd)), "testdata", "cilogs", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("fixture %s not captured on this machine", name)
	}
	return Clean(string(b))
}

// TestRealKorniaSetupFailureIsInfrastructure uses the actual log from the
// user's open PR. The macOS leg died with repeated HTTP 500s while installing
// its toolchain; v1 queued that for a human as though the patch were wrong.
func TestRealKorniaSetupFailureIsInfrastructure(t *testing.T) {
	v := ClassifyLog(fixture(t, "kornia-setup-pixi-500.log"))
	if v.Class != ClassInfra {
		t.Fatalf("class = %s, want infra; why=%q", v.Class, v.Why)
	}
	if !strings.Contains(v.Why, "5xx") {
		t.Errorf("why should name the cause: %q", v.Why)
	}
	t.Logf("verdict: %s -- %s", v.Class, v.Why)
}

// The collector job is literally `echo job failed && exit 1`. It should never
// have been reported as its own problem.
func TestCollectorIsRecognisedAsAMirror(t *testing.T) {
	if !IsMirror("collector") {
		t.Fatal("collector is a mirror job")
	}
	for _, n := range []string{"ci-ok", "all-green", "required-checks", "CI", "summary"} {
		if !IsMirror(n) {
			t.Errorf("%q should be a mirror", n)
		}
	}
	// But a real job with a similar-looking name must not be suppressed.
	for _, n := range []string{"tests (macos-latest, 3.12)", "build", "lint", "collector-tests"} {
		if IsMirror(n) {
			t.Errorf("%q is real work and must not be suppressed", n)
		}
	}
}

func TestClassifyLogSeparatesInfraFromReal(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		want      Class
	}{
		{"5xx", "Unexpected HTTP response: 500\nWaiting 20 seconds before trying again", ClassInfra},
		{"no disk", "write error: no space left on device", ClassInfra},
		{"dns", "Could not resolve host: proxy.golang.org", ClassInfra},
		{"cancelled", "##[error]The operation was canceled.", ClassInfra},
		{"runner died", "The runner has received a shutdown signal", ClassInfra},
		{"real test failure", "FAILED tests/test_core.py::test_svd - assert 1 != 1\n1 failed, 40 passed", ClassReal},
		{"real build failure", "./main.go:17:2: undefined: doThing", ClassReal},
		{"real lint failure", "error: unused variable `x`\nerror: aborting due to previous error", ClassReal},
		{"empty", "   ", ClassUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyLog(tc.log); got.Class != tc.want {
				t.Fatalf("class = %s want %s (why: %s)", got.Class, tc.want, got.Why)
			}
		})
	}
}

// Infrastructure must win: if the runner could not fetch its dependencies,
// anything else in the log is downstream of that.
func TestInfrastructureBeatsAnIncidentalTestWord(t *testing.T) {
	log := "Running tests...\nFAILED something\nUnexpected HTTP response: 503"
	if got := ClassifyLog(log); got.Class != ClassInfra {
		t.Fatalf("class = %s, want infra", got.Class)
	}
}

func TestParseDetailsURL(t *testing.T) {
	run, job, ok := ParseDetailsURL(
		"https://github.com/kornia/kornia/actions/runs/35201280132/job/105136503967")
	if !ok || run != 35201280132 || job != 105136503967 {
		t.Fatalf("run=%d job=%d ok=%v", run, job, ok)
	}
	if _, _, ok := ParseDetailsURL("https://example.com/other"); ok {
		t.Error("a non-Actions URL must not parse")
	}
}

func TestFailedStepIsTheFirstFailure(t *testing.T) {
	j := &Job{Steps: []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Number     int    `json:"number"`
	}{
		{Name: "Checkout", Conclusion: "success"},
		{Name: "Setup pixi", Conclusion: "failure"},
		{Name: "Run tests", Conclusion: "skipped"},
	}}
	if got := j.FailedStep(); got != "Setup pixi" {
		t.Fatalf("failed step = %q", got)
	}
}

// The decision that matters: a set of failures that is entirely
// environmental means wait, not push and not escalate.
func TestSummaryDrivesTheDecision(t *testing.T) {
	transient := Summary{Infra: []string{"tests (macos)"}, Mirror: []string{"collector"}}
	if transient.ShouldAttemptFix() {
		t.Error("infrastructure failures must not trigger a push")
	}
	if !transient.AllTransient() {
		t.Error("should be recognised as wait-and-see")
	}
	real := Summary{Real: []string{"unit"}, Infra: []string{"flaky"}}
	if !real.ShouldAttemptFix() {
		t.Error("a real failure alongside a flake still needs fixing")
	}
	if real.AllTransient() {
		t.Error("not all transient")
	}
}

func TestCleanStripsTimestampsAndColour(t *testing.T) {
	in := "2026-09-17T08:50:47.2532370Z \x1b[31m##[error]boom\x1b[0m\r\n"
	got := Clean(in)
	if strings.Contains(got, "2026-09-17T") || strings.Contains(got, "\x1b") {
		t.Fatalf("not cleaned: %q", got)
	}
	if !strings.Contains(got, "##[error]boom") {
		t.Fatalf("lost the message: %q", got)
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	got := Tail("a\n\nb\nc\n", 2)
	if got != "b\nc" {
		t.Fatalf("tail = %q", got)
	}
}
