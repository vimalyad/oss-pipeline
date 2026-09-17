// Package guard holds the checks that stand between a generated patch and a
// public pull request under the user's name.
//
// Every rule here exists because something got through. They are pure
// functions over paths and text so they can be tested exhaustively without a
// git repository, and so the expensive part (working out what a branch ships)
// is separable from the cheap part (deciding whether it may ship).
package guard

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Junk is build output that must never appear in a diff. uv.lock is listed
// separately because it is OUR fault: the test command creates it. Tooling
// used to validate a change must not leak into the change.
var Junk = regexp.MustCompile(
	`(^|/)(__pycache__/|\.pytest_cache/|node_modules/|\.DS_Store$|[^/]+\.pyc$|\.ruff_cache/)`)

// OurTooling is produced by this pipeline's own verification, not by the fix.
var OurTooling = map[string]bool{"uv.lock": true}

// AgentArtefacts is agent scaffolding. An auto-fix run once committed a
// 92-line agent workflow note into a real public PR: it was not build junk and
// not a workflow file, so nothing that existed at the time caught it.
var AgentArtefacts = regexp.MustCompile(
	`(?i)(^|/)(\.agents?|\.claude|\.cursor|\.aider[^/]*|\.github/copilot[^/]*)/` +
		`|(^|/)(SKILL|AGENTS|CLAUDE|GEMINI|\.cursorrules)\.md$` +
		`|(^|/)\.aider\.`)

type secretPattern struct {
	re   *regexp.Regexp
	name string
}

var secrets = []secretPattern{
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), "GitHub token"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), "GitHub fine-grained token"},
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "AWS access key"},
	{regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----`), "private key"},
	{regexp.MustCompile(`sk-[A-Za-z0-9]{32,}`), "API secret key"},
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`), "Anthropic API key"},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), "Slack token"},
}

// workEmail is the other account's address. Committing it under the OSS
// identity links the two accounts in public, permanently.
var workEmail = regexp.MustCompile(`(?i)vimal\.yadav@example\.com`)

// ScanSecrets names every kind of credential found in text.
func ScanSecrets(text string) []string {
	var found []string
	for _, s := range secrets {
		if s.re.MatchString(text) {
			found = append(found, s.name)
		}
	}
	if workEmail.MatchString(text) {
		found = append(found, "work email address")
	}
	return found
}

// BodyForbidden is language that must never reach a public PR description.
//
// The implementer's closing message is addressed to the pipeline operator, not
// to maintainers. Publishing it verbatim once put an agent transcript on a
// real PR: first-person notes about denied commands, a claim the tests had not
// been run when they had, and an admission of guessing a PR number.
var BodyForbidden = regexp.MustCompile(
	`(?i)\b(claude|anthropic|LLM|language model|AI|assistant|` +
		`sandbox|harness|requires approval|this session|` +
		`I could not|I was unable|I couldn't|I guessed|statically reviewed)\b`)

// CheckBody returns the forbidden phrases present in a PR body or comment.
func CheckBody(text string) []string {
	m := BodyForbidden.FindAllString(text, -1)
	seen := map[string]bool{}
	var out []string
	for _, s := range m {
		l := strings.ToLower(s)
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// Shipment is what a branch will actually put in front of maintainers.
//
// Computed as the NET set: files the branch adds or modifies versus upstream,
// minus anything the working tree deletes. Checking only uncommitted work is
// how agent scaffolding reached a public PR -- the rule matched the path
// perfectly, but the file had been committed already, so the check ran against
// an empty set and reported clean.
type Shipment struct {
	Files []string
	Diff  string
	// ExistingTopLevel is what the repo had before this branch.
	ExistingTopLevel map[string]bool
}

// Problem is one reason a branch must not be submitted.
type Problem struct {
	Path string
	Why  string
}

func (p Problem) String() string { return p.Why }

// Inspect applies every path and content rule to a shipment.
func Inspect(s Shipment) []Problem {
	var out []Problem

	if strings.TrimSpace(s.Diff) == "" {
		out = append(out, Problem{Why: "empty diff -- nothing to submit"})
	}
	for _, name := range ScanSecrets(s.Diff) {
		out = append(out, Problem{Why: fmt.Sprintf("diff contains a %s -- refusing to push", name)})
	}
	for _, d := range NewTopLevelDirs(s) {
		out = append(out, Problem{Path: d, Why: fmt.Sprintf(
			"diff creates a new top-level directory %q; a fix should not", d)})
	}
	for _, path := range s.Files {
		if path == "" {
			continue
		}
		switch {
		case strings.HasPrefix(path, ".github/workflows"):
			out = append(out, Problem{path, fmt.Sprintf(
				"diff touches %s; workflow changes are excluded", path)})
		case AgentArtefacts.MatchString(path):
			out = append(out, Problem{path, fmt.Sprintf(
				"diff adds agent tooling %s; never ship this", path)})
		case Junk.MatchString(path):
			out = append(out, Problem{path, fmt.Sprintf(
				"diff contains build artefact %s", path)})
		case OurTooling[path]:
			out = append(out, Problem{path, fmt.Sprintf(
				"diff contains %s, created by our own test tooling -- clean it before committing", path)})
		}
	}
	return out
}

// NewTopLevelDirs are directories the diff invents.
//
// A bug fix does not create a new top-level directory. When one appears it is
// almost always the implementer building scaffolding for itself.
func NewTopLevelDirs(s Shipment) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range s.Files {
		dir, _, ok := strings.Cut(p, "/")
		if !ok || dir == "" || s.ExistingTopLevel[dir] || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// nameStatus is one line of `git diff --name-status`.
type nameStatus struct {
	Status string
	Path   string
}

// parseNameStatus reads git's name-status output, taking the LAST
// tab-separated field as the path so a rename (which carries both the old and
// the new name) resolves to the destination.
func parseNameStatus(s string) []nameStatus {
	var out []nameStatus
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		st := strings.TrimSpace(fields[0])
		path := fields[len(fields)-1]
		if st == "" || path == "" {
			continue
		}
		out = append(out, nameStatus{Status: st, Path: path})
	}
	return out
}

// NetFiles computes the shipped set from git's two name-status listings: what
// the branch committed versus upstream, and what the working tree still has
// pending. A file committed earlier and deleted now is not something this PR
// adds, and a file added only in the working tree is.
func NetFiles(committedNameStatus, pendingNameStatus string) []string {
	set := map[string]bool{}
	for _, e := range parseNameStatus(committedNameStatus) {
		switch e.Status[:1] {
		case "A", "M", "R", "C":
			set[e.Path] = true
		}
	}
	// Collect pending adds and deletes separately, then subtract. Doing it in
	// one pass would make the result depend on the order git happens to list
	// the files in.
	pendingAdd, pendingDel := map[string]bool{}, map[string]bool{}
	for _, e := range parseNameStatus(pendingNameStatus) {
		if e.Status[:1] == "D" {
			pendingDel[e.Path] = true
		} else {
			pendingAdd[e.Path] = true
		}
	}
	for p := range pendingAdd {
		set[p] = true
	}
	for p := range pendingDel {
		delete(set, p)
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
