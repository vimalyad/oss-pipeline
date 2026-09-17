package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vimalyad/osspipeline/internal/model"
)

// root finds the pipeline root from the test's working directory.
func root(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Dir(filepath.Dir(wd)) // internal/store -> repo root
}

// TestLoadsEveryV1File is the parity gate for group 1.
//
// The Go build must read v1's existing state with no migration, so that both
// binaries can run against the same files and their output be compared. If
// this fails, the rewrite has diverged from the data it has to inherit.
func TestLoadsEveryV1File(t *testing.T) {
	s := New(root(t))
	if _, err := os.Stat(s.dir()); os.IsNotExist(err) {
		t.Skip("no v1 state on this machine")
	}
	ok, bad := s.All()
	for _, b := range bad {
		t.Errorf("could not load %s: %v", b.Slug, b.Err)
	}
	if len(ok) == 0 {
		t.Fatal("loaded no candidates at all")
	}
	t.Logf("loaded %d candidates, %d unreadable", len(ok), len(bad))

	// Spot-check that nested structures actually decoded, not just the
	// top-level scalars: a wrong tag on Brief would leave it nil and the
	// count above would still look healthy.
	var withBrief, withFacts, withSignal int
	for _, c := range ok {
		if c.Slug() == "" || c.Repo == "" || c.Issue == 0 {
			t.Errorf("degenerate candidate: %+v", c)
		}
		if c.Brief != nil {
			withBrief++
		}
		if c.Facts != nil {
			withFacts++
		}
		if c.PRSignal != nil {
			withSignal++
		}
	}
	if withBrief == 0 || withFacts == 0 {
		t.Errorf("no candidate decoded a Brief (%d) or Facts (%d) -- json tags are wrong",
			withBrief, withFacts)
	}
	t.Logf("with brief: %d, with facts: %d, with pr_signal: %d",
		withBrief, withFacts, withSignal)
}

// TestRoundTripPreservesEveryField guards the same property from the other
// side: writing a candidate and reading it back must not lose anything.
func TestRoundTripPreservesEveryField(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	n := 42
	want := &model.Candidate{
		Repo: "acme/widget", Issue: 7, Title: "t", URL: "u",
		Status: model.StatusChangesRequested,
		Labels: []string{"bug"}, Comments: 3, Reactions: 1,
		Contest:  model.ContestStalePR,
		PRSignal: &model.PRSignal{Number: 9, Author: "someone", Reasons: []string{"stale"}},
		Brief:    &model.Brief{MaintainerDesiredApproach: "do x", OpenQuestions: []string{}},
		Facts:    &model.RepoFacts{Repo: "acme/widget", Stars: 5, RequiresDCO: true},
		PRNumber: &n,
		History:  []model.HistoryEntry{{At: "now", From: "pr_open", To: "changes_requested"}},
	}
	if _, err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(want.Slug())
	if err != nil {
		t.Fatal(err)
	}
	if got.PRSignal == nil || got.PRSignal.Number != 9 {
		t.Error("pr_signal lost")
	}
	if got.Brief == nil || got.Brief.MaintainerDesiredApproach != "do x" {
		t.Error("brief lost")
	}
	if got.Facts == nil || !got.Facts.RequiresDCO {
		t.Error("facts lost")
	}
	if got.PRNumber == nil || *got.PRNumber != 42 {
		t.Error("pr_number lost")
	}
	if got.Status != model.StatusChangesRequested || got.Contest != model.ContestStalePR {
		t.Error("enums lost")
	}
}

func TestRejectsUnknownStatus(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "state", "candidates"), 0o755)
	os.WriteFile(filepath.Join(dir, "state", "candidates", "a__b__1.json"),
		[]byte(`{"repo":"a/b","issue":1,"status":"nonsense"}`), 0o644)
	if _, err := New(dir).Load("a__b__1"); err == nil {
		t.Fatal("a hand-edited bad status must be reported, not accepted")
	}
}
