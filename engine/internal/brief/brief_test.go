package brief

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const thread = `REPO: kornia/kornia (Python, 11346 stars)
ISSUE #4201: MPS svd fails
LABELS: bug

--- opened by reporter [NONE] at 2026-01-01T00:00:00Z ---
batched linalg.svd returns garbage on Apple silicon.

--- comment by stranger [NONE] at 2026-01-02T00:00:00Z (0 reactions) https://c1 ---
Just cast everything to float64 before the call, that fixes it for me.

--- comment by ducha-aiki [OWNER] at 2026-01-03T00:00:00Z (4 reactions) https://c2 ---
Let's document the limit and raise a clear error rather than silently
falling back. We do not want a float64 cast in the hot path.
`

type fakeJudge struct {
	out    string
	err    error
	prompt string
	system string
	stdin  string
}

func (f *fakeJudge) JudgeWith(_ context.Context, prompt, system, stdin string) (string, error) {
	f.prompt, f.system, f.stdin = prompt, system, stdin
	return f.out, f.err
}

func answer(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestExtractPassesTheThreadOnStdin(t *testing.T) {
	j := &fakeJudge{out: answer(map[string]any{})}
	if _, err := Extract(context.Background(), j, thread, ""); err != nil {
		t.Fatal(err)
	}
	if j.stdin != thread {
		t.Error("the thread did not reach stdin")
	}
	if !strings.Contains(j.system, "never infer") {
		t.Errorf("system prompt = %q", j.system)
	}
	// The bot list must be interpolated, not left as a placeholder.
	if strings.Contains(j.prompt, "{{bots}}") || !strings.Contains(j.prompt, "coderabbitai") {
		t.Errorf("bot list not rendered into the prompt")
	}
}

// TestOnlyAMaintainerCanSetTheApproach: a CONTRIBUTOR or NONE comment is an
// opinion however confident, and promoting the best-sounding one is how a
// pipeline implements a stranger's suggestion.
func TestOnlyAMaintainerCanSetTheApproach(t *testing.T) {
	tests := []struct {
		assoc string
		keep  bool
	}{
		{"OWNER", true}, {"MEMBER", true}, {"COLLABORATOR", true},
		{"owner", true}, // case is the model's, not a signal
		{"CONTRIBUTOR", false}, {"NONE", false}, {"", false}, {"MAINTAINER", false},
	}
	for _, tt := range tests {
		t.Run(tt.assoc, func(t *testing.T) {
			r, err := Parse(answer(map[string]any{
				"maintainer_desired_approach": "Let's document the limit and raise a clear error rather than silently",
				"approach_source_url":         "https://c2",
				"approach_author_association": tt.assoc,
			}), thread)
			if err != nil {
				t.Fatal(err)
			}
			got := r.Brief.MaintainerDesiredApproach != ""
			if got != tt.keep {
				t.Fatalf("kept = %v, want %v", got, tt.keep)
			}
			if !tt.keep {
				if r.Brief.ApproachSourceURL != "" {
					t.Error("the source URL outlived the approach it pointed at")
				}
				if len(r.Dropped) == 0 {
					t.Error("a silent drop is indistinguishable from a careful model")
				}
			}
		})
	}
}

// TestAFabricatedQuoteIsDiscarded is the check that matters most. Every later
// stage treats maintainer_desired_approach as the specification, and nothing
// downstream still has the thread to check it against.
func TestAFabricatedQuoteIsDiscarded(t *testing.T) {
	r, err := Parse(answer(map[string]any{
		"maintainer_desired_approach": "Please add a new dependency on scipy and rewrite the solver",
		"approach_author_association": "OWNER",
		"approach_source_url":         "https://c2",
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if r.Brief.MaintainerDesiredApproach != "" {
		t.Fatalf("kept an invented quote: %q", r.Brief.MaintainerDesiredApproach)
	}
	if len(r.Dropped) == 0 || !strings.Contains(r.Dropped[0], "not in the thread") {
		t.Errorf("dropped = %v", r.Dropped)
	}
}

func TestARealQuoteSurvivesWrapping(t *testing.T) {
	// The thread wraps this across two lines; a model reproducing it on one
	// line is not fabricating anything.
	r, err := Parse(answer(map[string]any{
		"maintainer_desired_approach": "Let's document the limit and raise a clear error rather than silently falling back.",
		"approach_author_association": "OWNER",
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if r.Brief.MaintainerDesiredApproach == "" {
		t.Fatalf("a genuine quote was rejected: %v", r.Dropped)
	}
}

func TestAnElidedQuoteIsCheckedSegmentBySegment(t *testing.T) {
	ok, err := Parse(answer(map[string]any{
		"maintainer_desired_approach": "Let's document the limit ... We do not want a float64 cast in the hot path.",
		"approach_author_association": "OWNER",
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Brief.MaintainerDesiredApproach == "" {
		t.Fatalf("a legitimately elided quote was rejected: %v", ok.Dropped)
	}

	// Order matters: the elision may not be used to stitch together text that
	// never appeared in that sequence.
	bad, err := Parse(answer(map[string]any{
		"maintainer_desired_approach": "We do not want a float64 cast in the hot path. ... Let's document the limit and raise",
		"approach_author_association": "OWNER",
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if bad.Brief.MaintainerDesiredApproach != "" {
		t.Error("an out-of-order stitch passed the check")
	}
}

// TestRejectedApproachesAreNotVerifiedVerbatim: rule 3 asks the model to
// rewrite each refusal as a self-contained prohibition, because it is later
// checked against a diff by something with no access to this thread. Verifying
// them verbatim would discard exactly the rewriting that was requested -- on
// the briefs already stored it rejected fourteen of twenty-nine, all genuine.
func TestRejectedApproachesAreNotVerifiedVerbatim(t *testing.T) {
	r, err := Parse(answer(map[string]any{
		"rejected_approaches": []string{
			// Verbatim from the thread.
			"We do not want a float64 cast in the hot path.",
			// The rewritten form rule 3 actually asks for: the maintainer
			// said it, but not in these words.
			"do not cast to float64 in the hot path; document the limit instead",
		},
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Brief.RejectedApproaches) != 2 {
		t.Fatalf("kept %v; a rewritten prohibition is what rule 3 asked for",
			r.Brief.RejectedApproaches)
	}
	if len(r.Dropped) != 0 {
		t.Errorf("dropped %v", r.Dropped)
	}
}

// TestAQuoteFromTheTrimmedPartIsStillVerified: the prompt gets a bounded
// transcript, so a quote may legitimately come from the part trimmed out of
// it. Checking against the trimmed text discarded a real maintainer statement
// from a 60,000-character thread.
func TestAQuoteFromTheTrimmedPartIsStillVerified(t *testing.T) {
	trimmed := "REPO: a/b\n[... middle of thread truncated ...]\ntail only\n"
	full := thread
	r, err := Parse(answer(map[string]any{
		"maintainer_desired_approach": "Let's document the limit and raise a clear error rather than silently",
		"approach_author_association": "OWNER",
	}), full)
	if err != nil {
		t.Fatal(err)
	}
	if r.Brief.MaintainerDesiredApproach == "" {
		t.Fatal("verifying against the full thread still rejected a real quote")
	}
	// And against the trimmed text it would have been thrown away, which is
	// the bug this guards.
	r2, _ := Parse(answer(map[string]any{
		"maintainer_desired_approach": "Let's document the limit and raise a clear error rather than silently",
		"approach_author_association": "OWNER",
	}), trimmed)
	if r2.Brief.MaintainerDesiredApproach != "" {
		t.Skip("trimmed text happened to contain the quote; nothing to assert")
	}
}

// TestMarkdownDecorationIsNotAFabrication covers the three shapes the audit
// found: a blockquote, backticked code, and list bullets.
func TestMarkdownDecorationIsNotAFabrication(t *testing.T) {
	quoting := `--- comment by m [OWNER] at t (0 reactions) u ---
> This rule bans assignment from one type to another, if:
> * the destination type has an optional property, and
> * the source type has no matching property.

Agreed. Also the ` + "`sort_keys=True`" + ` flag was breaking the order.
`
	for _, q := range []string{
		"This rule bans assignment from one type to another, if:\n* the destination type has an optional property, and\n* the source type has no matching property.",
		"the sort_keys=True flag was breaking the order",
	} {
		r, err := Parse(answer(map[string]any{
			"maintainer_desired_approach": q, "approach_author_association": "OWNER",
		}), quoting)
		if err != nil {
			t.Fatal(err)
		}
		if r.Brief.MaintainerDesiredApproach == "" {
			t.Errorf("markdown decoration made a genuine quote look invented: %q\n  %v", q, r.Dropped)
		}
	}
}

// TestAllThreeElisionMarkers: models elide with [...] and the ellipsis
// character as readily as with three dots.
func TestAllThreeElisionMarkers(t *testing.T) {
	for _, marker := range []string{"...", "[...]", "…", "[…]"} {
		q := "Let's document the limit " + marker + " We do not want a float64 cast in the hot path."
		r, err := Parse(answer(map[string]any{
			"maintainer_desired_approach": q, "approach_author_association": "OWNER",
		}), thread)
		if err != nil {
			t.Fatal(err)
		}
		if r.Brief.MaintainerDesiredApproach == "" {
			t.Errorf("elision marker %q was not understood: %v", marker, r.Dropped)
		}
	}
}

func TestABotCannotClaimAnIssue(t *testing.T) {
	for _, login := range []string{"github-actions[bot]", "CodeRabbitAI", "dependabot", "renovate"} {
		r, err := Parse(answer(map[string]any{"claimed_by": login, "claimed_at": "2026-01-01"}), thread)
		if err != nil {
			t.Fatal(err)
		}
		if r.Brief.ClaimedBy != "" || r.Brief.ClaimedAt != "" {
			t.Errorf("%s claimed the issue", login)
		}
	}
	r, _ := Parse(answer(map[string]any{"claimed_by": "a-real-person", "claimed_at": "2026-01-01"}), thread)
	if r.Brief.ClaimedBy != "a-real-person" {
		t.Error("a human claim was discarded")
	}
}

// TestEmptyIsAValidAnswer: an empty approach correctly fails the scorer, and
// papering over it with a plausible guess is the failure this stage exists to
// avoid.
func TestEmptyIsAValidAnswer(t *testing.T) {
	r, err := Parse(answer(map[string]any{}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if r.Brief.MaintainerDesiredApproach != "" || len(r.Brief.OpenQuestions) != 0 {
		t.Fatalf("invented content: %+v", r.Brief)
	}
	if len(r.Dropped) != 0 {
		t.Errorf("a genuinely empty answer reported drops: %v", r.Dropped)
	}
}

func TestBlankListEntriesAreRemoved(t *testing.T) {
	r, err := Parse(answer(map[string]any{
		"acceptance_criteria": []string{"the test passes", "", "   "},
		"open_questions":      []string{},
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Brief.AcceptanceCriteria) != 1 {
		t.Fatalf("= %v", r.Brief.AcceptanceCriteria)
	}
}

func TestAnswerWrappedInProseOrFences(t *testing.T) {
	for _, wrapped := range []string{
		"```json\n{\"reproduction\": \"run pytest\"}\n```",
		"Here is the JSON:\n{\"reproduction\": \"run pytest\"}",
		"{\"reproduction\": \"run pytest\"}\nHope that helps.",
	} {
		r, err := Parse(wrapped, thread)
		if err != nil {
			t.Fatalf("%q: %v", wrapped, err)
		}
		if r.Brief.Reproduction != "run pytest" {
			t.Errorf("%q -> %q", wrapped, r.Brief.Reproduction)
		}
	}
}

func TestUnusableAnswerIsAnError(t *testing.T) {
	for _, bad := range []string{"", "I could not find anything.", "{unterminated"} {
		if _, err := Parse(bad, thread); !errors.Is(err, ErrBrief) {
			t.Errorf("%q -> err = %v, want ErrBrief", bad, err)
		}
	}
}

func TestEmptyTranscriptIsRefused(t *testing.T) {
	if _, err := Extract(context.Background(), &fakeJudge{}, "   ", ""); !errors.Is(err, ErrBrief) {
		t.Fatalf("err = %v", err)
	}
}

// TestShortFragmentsAreNotUsedToBypassTheCheck: "..." between two-word
// fragments would otherwise match almost any thread.
func TestShortFragmentsAreNotUsedToBypassTheCheck(t *testing.T) {
	r, err := Parse(answer(map[string]any{
		"maintainer_desired_approach": "add ... scipy ... rewrite",
		"approach_author_association": "OWNER",
	}), thread)
	if err != nil {
		t.Fatal(err)
	}
	// Every segment is too short to be evidence, so nothing was verified; the
	// quote is kept only because there is nothing to disprove. What must not
	// happen is a long fabrication sneaking through alongside short ones.
	r2, _ := Parse(answer(map[string]any{
		"maintainer_desired_approach": "add ... please add a new dependency on scipy and rewrite the solver",
		"approach_author_association": "OWNER",
	}), thread)
	if r2.Brief.MaintainerDesiredApproach != "" {
		t.Error("a long fabricated segment passed because a short one preceded it")
	}
	_ = r
}
