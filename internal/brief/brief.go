// Package brief distils an issue thread into the specification a patch is
// written against.
//
// This is the highest-risk stage in the pipeline. A misread thread produces a
// confidently wrong pull request, which costs a maintainer their review time
// and reads as careless, so the prompt is written to make *absence* the easy
// answer: every field may be empty, nothing may be inferred, and quotes must
// be verbatim. An empty maintainer_desired_approach correctly fails the scorer
// rather than being papered over with a plausible guess.
//
// Two checks run after the model answers, because instructions in a prompt are
// a request and the code is the enforcement.
package brief

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vimalyad/osspipeline/internal/llm"
	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/text"
)

//go:embed prompts/extract.md
var extractPrompt string

//go:embed prompts/system.md
var systemPrompt string

var ErrBrief = errors.New("brief")

// Judge runs the distillation with the thread on stdin.
type Judge interface {
	JudgeWith(ctx context.Context, prompt, system, stdin string) (string, error)
}

// botMarkers are logins that review and comment like maintainers but carry no
// authority. Treating a bot's suggestion as the specification is a real
// failure mode, not a hypothetical one: a Codex bot review turned up on a live
// thread during development.
var botMarkers = []string{
	"[bot]", "coderabbitai", "github-actions", "dependabot", "codex",
	"sonarcloud", "codecov", "sweep-ai", "renovate", "greptile", "cursor",
}

// maintainerAssociations are the only ones whose word is the specification.
var maintainerAssociations = map[string]bool{
	"OWNER": true, "MEMBER": true, "COLLABORATOR": true,
}

// Result is the brief plus what was discarded reaching it.
type Result struct {
	Brief model.Brief
	// Dropped records every field the post-checks removed, with the reason.
	// It belongs in the audit trail: a brief that is empty because the model
	// was careful and one that is empty because it invented a quote look
	// identical from the outside.
	Dropped []string
}

// Extract distils a rendered thread.
// Extract distils a rendered thread.
//
// full is the same thread with no length cap, used only for verifying quotes.
// The two differ because the prompt gets a bounded transcript and a quote may
// legitimately come from the part that was trimmed out of it -- checking
// against the trimmed text discarded a real maintainer statement from a
// 60,000-character thread. Pass "" when the transcript was not truncated.
func Extract(ctx context.Context, j Judge, transcript, full string) (Result, error) {
	if j == nil {
		return Result{}, fmt.Errorf("%w: no judge", ErrBrief)
	}
	if strings.TrimSpace(transcript) == "" {
		return Result{}, fmt.Errorf("%w: empty transcript", ErrBrief)
	}
	prompt := llm.Render(extractPrompt, map[string]string{
		"bots": strings.Join(botMarkers, ", "),
	})
	out, err := j.JudgeWith(ctx, prompt, systemPrompt, transcript)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBrief, err)
	}
	if strings.TrimSpace(full) == "" {
		full = transcript
	}
	return Parse(out, full)
}

type rawBrief struct {
	MaintainerDesiredApproach string   `json:"maintainer_desired_approach"`
	ApproachSourceURL         string   `json:"approach_source_url"`
	ApproachAuthorAssociation string   `json:"approach_author_association"`
	RejectedApproaches        []string `json:"rejected_approaches"`
	AcceptanceCriteria        []string `json:"acceptance_criteria"`
	OpenQuestions             []string `json:"open_questions"`
	ClaimedBy                 string   `json:"claimed_by"`
	ClaimedAt                 string   `json:"claimed_at"`
	Reproduction              string   `json:"reproduction"`
}

// Parse decodes a model answer and enforces the rules the prompt asked for.
// Separated from Extract so the enforcement can be tested without a model.
func Parse(answer, transcript string) (Result, error) {
	blob, err := llm.ExtractJSON(answer)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBrief, err)
	}
	var raw rawBrief
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBrief, err)
	}

	var r Result
	assoc := strings.ToUpper(strings.TrimSpace(raw.ApproachAuthorAssociation))

	// Enforcement 1: authority. The prompt says only a maintainer's word may
	// fill this field; the bar is cheap to check, so it is checked.
	if raw.MaintainerDesiredApproach != "" && !maintainerAssociations[assoc] {
		r.Dropped = append(r.Dropped, fmt.Sprintf(
			"approach attributed to %q, which is not OWNER, MEMBER or COLLABORATOR",
			orElse(assoc, "nothing")))
		raw.MaintainerDesiredApproach, raw.ApproachSourceURL = "", ""
		assoc = ""
	}

	// Enforcement 2: the quote has to exist. A fabricated maintainer approach
	// is the single most expensive thing this stage can produce -- every later
	// stage treats it as the specification, and nothing downstream has the
	// thread to check it against. Rule 5 asks for verbatim quotes; this is
	// what makes the ask real.
	if raw.MaintainerDesiredApproach != "" && !quoted(transcript, raw.MaintainerDesiredApproach) {
		r.Dropped = append(r.Dropped, fmt.Sprintf(
			"approach quote is not in the thread: %q", text.Ellipsis(raw.MaintainerDesiredApproach, 120)))
		raw.MaintainerDesiredApproach, raw.ApproachSourceURL, assoc = "", "", ""
	}
	// rejected_approaches are deliberately NOT verified. Rule 3 of the prompt
	// asks for them to be rewritten as self-contained prohibitions, because
	// they are later checked against a diff by something with no access to
	// this thread -- "keep the maintainer's wording, but make the subject
	// explicit". Verifying them verbatim would discard exactly the rewriting
	// that was asked for: on the stored briefs it rejected fourteen of
	// twenty-nine, all of them legitimate.

	// A bot cannot claim an issue. Honouring one would hold a candidate
	// forever for a claim nobody made.
	if isBot(raw.ClaimedBy) {
		r.Dropped = append(r.Dropped, fmt.Sprintf("claim attributed to the bot %q", raw.ClaimedBy))
		raw.ClaimedBy, raw.ClaimedAt = "", ""
	}

	r.Brief = model.Brief{
		MaintainerDesiredApproach: raw.MaintainerDesiredApproach,
		ApproachSourceURL:         raw.ApproachSourceURL,
		ApproachAuthorAssociation: assoc,
		RejectedApproaches:        clean(raw.RejectedApproaches),
		AcceptanceCriteria:        clean(raw.AcceptanceCriteria),
		OpenQuestions:             clean(raw.OpenQuestions),
		ClaimedBy:                 raw.ClaimedBy,
		ClaimedAt:                 raw.ClaimedAt,
		Reproduction:              raw.Reproduction,
	}
	return r, nil
}

// quoted reports whether a quote really appears in the thread.
//
// Auditing the briefs the Python implementation had already produced is what
// shaped this. A naive comparison called four of thirty-six maintainer
// approaches fabricated; every one turned out to be genuine, and the check was
// the thing that was wrong:
//
//   - a maintainer quoting an earlier comment appears in the thread with "> "
//     prefixes on every line, which the model reasonably strips
//   - markdown decoration differs: the thread has `sort_keys=True` in
//     backticks and the quote does not
//   - models elide with "[...]" and "..." as readily as with "..."
//
// So the comparison strips markdown decoration and treats all three elision
// markers alike. It is still a real check -- an invented sentence has no
// source to normalise towards -- but it now errs towards believing the model,
// because the cost of a false positive here is silently discarding a
// maintainer's actual words.
func quoted(transcript, quote string) bool {
	hay := normalise(transcript)
	pos := 0
	for _, seg := range splitElisions(quote) {
		n := normalise(seg)
		// A fragment of a few characters matches anything; requiring a real
		// span is what stops an elision from being a way around the check.
		if len([]rune(n)) < 12 {
			continue
		}
		i := strings.Index(hay[pos:], n)
		if i < 0 {
			return false
		}
		pos += i + len(n)
	}
	return true
}

var elisionRe = regexp.MustCompile(`\[\s*(\.\.\.|…)\s*\]|\.\.\.|…`)

func splitElisions(s string) []string { return elisionRe.Split(s, -1) }

// markdownNoise is the decoration that differs between a thread and a faithful
// quotation of it: blockquote markers, list bullets, emphasis and code ticks.
var markdownNoise = regexp.MustCompile("[`*_>#]+")

func normalise(s string) string {
	s = markdownNoise.ReplaceAllString(s, "")
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func isBot(login string) bool {
	low := strings.ToLower(login)
	if low == "" {
		return false
	}
	for _, m := range botMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

func clean(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
