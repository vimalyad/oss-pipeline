// Package repofacts collects the repository-level gates that decide whether a
// project is worth approaching at all.
//
// They change slowly and cost API calls, so they are fetched once per
// repository per week rather than once per candidate.
//
// Policy detection is a cheap keyword prefilter followed by an adjudication
// only when a keyword actually fires. The keywords alone cannot tell "we
// require disclosure of AI assistance" from "we do not accept AI-generated
// contributions", and that distinction decides whether the repository is
// usable at all -- and, separately, whether the autonomous path may ever touch
// it, since a disclosure asserts a human review that an unattended run did not
// perform.
package repofacts

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/llm"
	"github.com/vimalyad/osspipeline/internal/model"
)

//go:embed prompts/ai_policy.md
var aiPolicyPrompt string

//go:embed prompts/eligibility.md
var eligibilityPrompt string

var ErrRepoFacts = errors.New("repofacts")

// CacheDays is how long facts are trusted before a refetch.
const CacheDays = 7

// API is the GitHub surface this package needs: plain GETs and one search.
// Declared here rather than imported so the package can be tested with a map.
type API interface {
	Get(ctx context.Context, path string) (string, error)
	GraphQL(ctx context.Context, query string, vars map[string]any, v any) error
}

// Judge adjudicates the two questions keywords cannot answer.
type Judge interface {
	JudgeJSON(ctx context.Context, prompt string, v any) error
}

// Cache is the weekly store of facts.
type Cache interface {
	LoadRepoFacts(repo string) (*model.RepoFacts, error)
	SaveRepoFacts(f *model.RepoFacts) error
}

// policyFiles are every place a project states its contribution rules.
var policyFiles = []string{
	"CONTRIBUTING.md", ".github/CONTRIBUTING.md", "docs/CONTRIBUTING.md",
	".github/PULL_REQUEST_TEMPLATE.md", ".github/pull_request_template.md",
	"CONTRIBUTING.rst", "AGENTS.md", ".github/COPILOT_INSTRUCTIONS.md",
}

var (
	aiKeywords = regexp.MustCompile(`(?i)\b(ai[- ]generated|ai[- ]assisted|generative ai|llm|` +
		`chatgpt|copilot|claude|machine[- ]generated|artificial intelligence|ai tool)\b`)
	dcoKeywords = regexp.MustCompile(`(?i)(signed-off-by|developer certificate of origin|\bDCO\b)`)
	// (?!SS) keeps "CLASS" from matching "CLA".
	claKeywords = regexp.MustCompile(`(?i)(contributor licen[cs]e agreement|\bCLA\b)`)
	claFalse    = regexp.MustCompile(`(?i)\bCLASS\b`)
)

var testPaths = map[string]bool{
	"tests": true, "test": true, "spec": true, "__tests__": true, "t": true,
	"pytest.ini": true, "tox.ini": true, "noxfile.py": true, "conftest.py": true,
	"jest.config.js": true, "vitest.config.ts": true, "vitest.config.js": true,
}

// Options tunes a fetch.
type Options struct {
	Refresh          bool
	FirstTimeWindow  int
	Now              func() time.Time
	MaxDocBytes      int
	MaxExcerptWindow int
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Fetch returns the facts for a repository, from cache when they are fresh.
func Fetch(ctx context.Context, api API, j Judge, c Cache, repo string, o Options) (*model.RepoFacts, error) {
	if o.FirstTimeWindow == 0 {
		o.FirstTimeWindow = 90
	}
	if o.MaxDocBytes == 0 {
		o.MaxDocBytes = 40000
	}
	if o.MaxExcerptWindow == 0 {
		o.MaxExcerptWindow = 600
	}
	now := o.now()

	if !o.Refresh && c != nil {
		if cached, err := c.LoadRepoFacts(repo); err == nil && cached != nil && cached.FetchedAt != "" {
			if t, err := time.Parse(time.RFC3339, cached.FetchedAt); err == nil {
				if now.Sub(t) < CacheDays*24*time.Hour {
					return cached, nil
				}
			}
		}
	}

	raw, err := api.Get(ctx, "repos/"+repo)
	if err != nil {
		return nil, fmt.Errorf("%w: %s metadata: %v", ErrRepoFacts, repo, err)
	}
	var meta struct {
		Stars    int      `json:"stargazers_count"`
		Language string   `json:"language"`
		Topics   []string `json:"topics"`
		Archived bool     `json:"archived"`
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, fmt.Errorf("%w: %s metadata was not JSON: %v", ErrRepoFacts, repo, err)
	}

	docs, hasContributing := readPolicyDocs(ctx, api, repo)
	blob := strings.Join(docs, "\n\n")
	if len(blob) > o.MaxDocBytes {
		blob = blob[:o.MaxDocBytes]
	}

	f := &model.RepoFacts{
		Repo:            repo,
		Stars:           meta.Stars,
		PrimaryLanguage: meta.Language,
		Topics:          normaliseTopics(meta.Topics),
		HasContributing: hasContributing,
		HasTests:        hasTests(ctx, api, repo),
		FetchedAt:       now.UTC().Format(time.RFC3339),
	}
	if blob != "" {
		f.RequiresDCO = dcoKeywords.MatchString(blob)
		f.RequiresCLA = claKeywords.MatchString(claFalse.ReplaceAllString(blob, ""))
	}

	// The prefilter is the point: an adjudication per repository per week is
	// affordable only because most repositories never mention AI at all.
	if blob != "" && aiKeywords.MatchString(blob) {
		f.BansAIPRs, f.RequiresAIDisclosure, f.AIPolicyQuote =
			adjudicateAIPolicy(ctx, j, excerpts(blob, o.MaxExcerptWindow))
	}
	if blob != "" {
		f.RequiredIssueLabels, f.ForbiddenIssueLabels, f.EligibilityQuote =
			eligibility(ctx, j, blob)
	}
	f.MergedFirstTimePR90d = mergedFirstTimePR(ctx, api, repo, o.FirstTimeWindow, now)

	if c != nil {
		if err := c.SaveRepoFacts(f); err != nil {
			return f, fmt.Errorf("%w: caching %s: %v", ErrRepoFacts, repo, err)
		}
	}
	return f, nil
}

func readPolicyDocs(ctx context.Context, api API, repo string) ([]string, bool) {
	var docs []string
	var hasContributing bool
	for _, path := range policyFiles {
		body, ok := readFile(ctx, api, repo, path)
		if !ok || strings.TrimSpace(body) == "" {
			continue
		}
		docs = append(docs, "### "+path+"\n"+body)
		if strings.Contains(strings.ToLower(path), "contributing") {
			hasContributing = true
		}
	}
	return docs, hasContributing
}

func readFile(ctx context.Context, api API, repo, path string) (string, bool) {
	raw, err := api.Get(ctx, "repos/"+repo+"/contents/"+path)
	if err != nil {
		return "", false // a missing file is the common case, not an error.
	}
	var f struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if json.Unmarshal([]byte(raw), &f) != nil || f.Encoding != "base64" {
		return "", false
	}
	// GitHub wraps base64 at 60 columns.
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// adjudicateAIPolicy fails closed.
//
// An unknown policy is not permission. Assuming disclosure is required costs a
// line in a PR body when we were wrong; assuming it is not costs an
// undisclosed contribution to a project that asked, which is the kind of
// mistake that ends a contributor relationship rather than delaying one.
func adjudicateAIPolicy(ctx context.Context, j Judge, docs string) (bans, disclose bool, quote string) {
	if j == nil {
		return false, true, "(no adjudicator available; assuming disclosure required)"
	}
	var out struct {
		Bans               bool   `json:"bans"`
		RequiresDisclosure bool   `json:"requires_disclosure"`
		Quote              string `json:"quote"`
	}
	if err := j.JudgeJSON(ctx, llm.Render(aiPolicyPrompt, map[string]string{"docs": docs}), &out); err != nil {
		return false, true, "(policy adjudication failed; assuming disclosure required)"
	}
	return out.Bans, out.RequiresDisclosure, strings.TrimSpace(out.Quote)
}

// eligibility extracts which issues a project will accept an outside pull
// request for at all.
//
// cli/cli states "We accept external pull requests only for issues labelled
// `help wanted`". v1 read that same file and mined it only for AI, DCO and CLA
// keywords, so a pull request went out against a `bug`-labelled issue and was
// closed within half an hour by a rule the project documents plainly.
//
// This one fails open: an empty list means "no stated rule", which is the
// common and correct answer, and inventing a requirement would reject every
// candidate in the repository.
func eligibility(ctx context.Context, j Judge, docs string) (required, forbidden []string, quote string) {
	if j == nil {
		return nil, nil, ""
	}
	var out struct {
		Required  []string `json:"required_issue_labels"`
		Forbidden []string `json:"forbidden_issue_labels"`
		Quote     string   `json:"quote"`
	}
	if err := j.JudgeJSON(ctx, llm.Render(eligibilityPrompt, map[string]string{"docs": docs}), &out); err != nil {
		return nil, nil, ""
	}
	return normaliseLabels(out.Required), normaliseLabels(out.Forbidden), strings.TrimSpace(out.Quote)
}

// hasTests looks for a test directory or marker file, and falls back to the
// presence of any CI workflow.
//
// The fallback is deliberately weak and known to be: it answers "this project
// runs something in CI", not "this project has tests we can run". The strong
// answer needs a clone, which is internal/recipe's job, and a repository that
// reaches that stage gets the better check there.
func hasTests(ctx context.Context, api API, repo string) bool {
	if raw, err := api.Get(ctx, "repos/"+repo+"/contents/"); err == nil {
		var entries []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(raw), &entries) == nil {
			for _, e := range entries {
				if testPaths[strings.ToLower(e.Name)] {
					return true
				}
			}
		}
	}
	raw, err := api.Get(ctx, "repos/"+repo+"/contents/.github/workflows")
	if err != nil {
		return false
	}
	var wf []json.RawMessage
	return json.Unmarshal([]byte(raw), &wf) == nil && len(wf) > 0
}

const firstTimeQuery = `
query($q:String!) {
  search(query:$q, type:ISSUE, first:40) {
    nodes { ... on PullRequest { authorAssociation } }
  }
}`

// mergedFirstTimePR reports whether the project has merged a pull request from
// someone outside it recently. It is the single best predictor that a first
// contribution can land at all.
func mergedFirstTimePR(ctx context.Context, api API, repo string, days int, now time.Time) bool {
	since := now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
	var resp struct {
		Search struct {
			Nodes []struct {
				AuthorAssociation string `json:"authorAssociation"`
			} `json:"nodes"`
		} `json:"search"`
	}
	q := fmt.Sprintf("repo:%s is:pr is:merged merged:>=%s", repo, since)
	if err := api.GraphQL(ctx, firstTimeQuery, map[string]any{"q": q}, &resp); err != nil {
		return false
	}
	for _, n := range resp.Search.Nodes {
		switch n.AuthorAssociation {
		case "FIRST_TIME_CONTRIBUTOR", "CONTRIBUTOR", "NONE":
			return true
		}
	}
	return false
}

// excerpts returns the windows around each keyword match rather than the whole
// document, so a 200 kB contributing guide costs a few kilobytes of prompt.
func excerpts(text string, window int) string {
	locs := aiKeywords.FindAllStringIndex(text, 6)
	if len(locs) == 0 {
		return ""
	}
	var out []string
	for _, loc := range locs {
		start := max(0, loc[0]-window)
		end := min(len(text), loc[1]+window)
		out = append(out, text[start:end])
	}
	return strings.Join(out, "\n\n---\n\n")
}

func normaliseLabels(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		l := strings.ToLower(strings.Trim(strings.TrimSpace(s), "`"))
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func normaliseTopics(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		l := strings.ToLower(strings.TrimSpace(s))
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
