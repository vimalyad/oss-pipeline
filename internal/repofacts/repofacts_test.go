package repofacts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
)

type fakeAPI struct {
	files   map[string]string // path -> decoded body
	meta    string
	entries map[string]string // contents listing path -> raw JSON
	gql     any
	gqlErr  error
	gets    []string
}

func (f *fakeAPI) Get(_ context.Context, path string) (string, error) {
	f.gets = append(f.gets, path)
	if strings.HasSuffix(path, "/contents/") || strings.Contains(path, "/contents/.github/workflows") {
		if raw, ok := f.entries[path]; ok {
			return raw, nil
		}
		return "", errors.New("404")
	}
	if strings.Contains(path, "/contents/") {
		p := path[strings.Index(path, "/contents/")+len("/contents/"):]
		body, ok := f.files[p]
		if !ok {
			return "", errors.New("404")
		}
		b, _ := json.Marshal(map[string]string{
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte(body)),
		})
		return string(b), nil
	}
	if f.meta == "" {
		return "", errors.New("no metadata")
	}
	return f.meta, nil
}

func (f *fakeAPI) GraphQL(_ context.Context, _ string, _ map[string]any, v any) error {
	if f.gqlErr != nil {
		return f.gqlErr
	}
	b, _ := json.Marshal(f.gql)
	return json.Unmarshal(b, v)
}

type fakeJudge struct {
	answers []any
	err     error
	calls   int
}

func (f *fakeJudge) JudgeJSON(_ context.Context, _ string, v any) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if len(f.answers) == 0 {
		return errors.New("no answer configured")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	b, _ := json.Marshal(a)
	return json.Unmarshal(b, v)
}

type fakeCache struct {
	loaded *model.RepoFacts
	saved  *model.RepoFacts
}

func (f *fakeCache) LoadRepoFacts(string) (*model.RepoFacts, error) { return f.loaded, nil }
func (f *fakeCache) SaveRepoFacts(x *model.RepoFacts) error         { f.saved = x; return nil }

var now = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func opts() Options { return Options{Now: func() time.Time { return now }} }

func metaJSON(stars int, lang string, topics ...string) string {
	b, _ := json.Marshal(map[string]any{
		"stargazers_count": stars, "language": lang, "topics": topics,
	})
	return string(b)
}

// TestTopicsAreKept is the gap the plan named: v1 fetched them with the rest of
// the metadata and threw them away, so every domain match had to be inferred
// from the language and the repository name.
func TestTopicsAreKept(t *testing.T) {
	api := &fakeAPI{meta: metaJSON(11346, "Python", "Computer-Vision", "pytorch", "pytorch", "")}
	f, err := Fetch(context.Background(), api, nil, nil, "kornia/kornia", opts())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"computer-vision", "pytorch"}
	if len(f.Topics) != 2 || f.Topics[0] != want[0] || f.Topics[1] != want[1] {
		t.Fatalf("topics = %v, want %v lowercased, deduplicated and sorted", f.Topics, want)
	}
	if f.Stars != 11346 || f.PrimaryLanguage != "Python" {
		t.Errorf("facts = %+v", f)
	}
}

// TestTheKeywordPrefilterGatesTheAdjudication: an adjudication per repository
// per week is affordable only because most repositories never mention AI.
func TestTheKeywordPrefilterGatesTheAdjudication(t *testing.T) {
	api := &fakeAPI{
		meta:  metaJSON(100, "Go"),
		files: map[string]string{"CONTRIBUTING.md": "Please run the tests before opening a PR."},
	}
	j := &fakeJudge{answers: []any{
		map[string]any{"required_issue_labels": []string{}, "forbidden_issue_labels": []string{}},
	}}
	f, err := Fetch(context.Background(), api, j, nil, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	// Eligibility always runs when there are docs; the AI adjudication must not.
	if j.calls != 1 {
		t.Fatalf("judge called %d times; only eligibility should have run", j.calls)
	}
	if f.BansAIPRs || f.RequiresAIDisclosure {
		t.Errorf("a doc with no AI keyword produced an AI verdict: %+v", f)
	}
}

func TestAKeywordTriggersTheAdjudication(t *testing.T) {
	api := &fakeAPI{
		meta: metaJSON(100, "Go"),
		files: map[string]string{
			"CONTRIBUTING.md": "We do not accept AI-generated contributions of any kind.",
		},
	}
	j := &fakeJudge{answers: []any{
		map[string]any{"bans": true, "requires_disclosure": false,
			"quote": "We do not accept AI-generated contributions of any kind."},
		map[string]any{},
	}}
	f, err := Fetch(context.Background(), api, j, nil, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	if !f.BansAIPRs || f.AIPolicyQuote == "" {
		t.Fatalf("facts = %+v", f)
	}
}

// TestUnknownPolicyFailsClosed: an unknown policy is not permission. Being
// wrong costs a line in a PR body; the other way costs an undisclosed
// contribution to a project that asked for one.
func TestUnknownPolicyFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		j    Judge
	}{
		{"adjudicator errored", &fakeJudge{err: errors.New("claude unavailable")}},
		{"no adjudicator at all", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{
				meta:  metaJSON(1, "Go"),
				files: map[string]string{"CONTRIBUTING.md": "Disclose any use of Copilot."},
			}
			f, err := Fetch(context.Background(), api, tt.j, nil, "a/b", opts())
			if err != nil {
				t.Fatal(err)
			}
			if !f.RequiresAIDisclosure {
				t.Fatal("an unreadable policy was treated as permission")
			}
			if f.BansAIPRs {
				t.Error("failing closed must not invent a ban, which would reject the repo outright")
			}
			if !strings.Contains(f.AIPolicyQuote, "assuming disclosure required") {
				t.Errorf("quote = %q; the reader must know this was assumed", f.AIPolicyQuote)
			}
		})
	}
}

// TestEligibilityFailsOpen is the opposite direction on purpose: inventing a
// label requirement would reject every candidate in the repository.
func TestEligibilityFailsOpen(t *testing.T) {
	api := &fakeAPI{
		meta:  metaJSON(1, "Go"),
		files: map[string]string{"CONTRIBUTING.md": "Open a PR."},
	}
	f, err := Fetch(context.Background(), api, &fakeJudge{err: errors.New("down")}, nil, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.RequiredIssueLabels) != 0 || len(f.ForbiddenIssueLabels) != 0 {
		t.Fatalf("invented a rule: %+v", f)
	}
}

// TestEligibilityRulesAreRead is the cli/cli lesson. v1 read the same file and
// mined it only for AI, DCO and CLA keywords, so a pull request went out
// against a bug-labelled issue and was closed within half an hour by a rule
// the project documents plainly.
func TestEligibilityRulesAreRead(t *testing.T) {
	api := &fakeAPI{
		meta: metaJSON(40000, "Go"),
		files: map[string]string{
			".github/CONTRIBUTING.md": "We accept external pull requests only for issues labelled `help wanted`.",
		},
	}
	j := &fakeJudge{answers: []any{
		map[string]any{
			"required_issue_labels":  []string{"`Help Wanted`", " help wanted ", ""},
			"forbidden_issue_labels": []string{"core"},
			"quote":                  "We accept external pull requests only for issues labelled `help wanted`.",
		},
	}}
	f, err := Fetch(context.Background(), api, j, nil, "cli/cli", opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.RequiredIssueLabels) != 1 || f.RequiredIssueLabels[0] != "help wanted" {
		t.Fatalf("required = %v; backticks, case and duplicates must be normalised",
			f.RequiredIssueLabels)
	}
	if len(f.ForbiddenIssueLabels) != 1 || f.ForbiddenIssueLabels[0] != "core" {
		t.Errorf("forbidden = %v", f.ForbiddenIssueLabels)
	}
	if f.EligibilityQuote == "" {
		t.Error("the quote is what lets a human check the rule was read correctly")
	}
}

func TestDCOAndCLAKeywords(t *testing.T) {
	tests := []struct {
		name, doc        string
		wantDCO, wantCLA bool
	}{
		{"dco", "All commits must carry a Signed-off-by line.", true, false},
		{"cla", "You must sign our Contributor License Agreement.", false, true},
		{"cla abbreviation", "Sign the CLA before contributing.", false, true},
		// "CLASS" must not read as "CLA".
		{"class is not cla", "Add the new CLASS to the registry.", false, false},
		{"neither", "Run the tests.", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{meta: metaJSON(1, "Go"), files: map[string]string{"CONTRIBUTING.md": tt.doc}}
			f, err := Fetch(context.Background(), api, &fakeJudge{answers: []any{map[string]any{}}}, nil, "a/b", opts())
			if err != nil {
				t.Fatal(err)
			}
			if f.RequiresDCO != tt.wantDCO || f.RequiresCLA != tt.wantCLA {
				t.Errorf("dco=%v cla=%v, want %v and %v",
					f.RequiresDCO, f.RequiresCLA, tt.wantDCO, tt.wantCLA)
			}
		})
	}
}

func TestFreshCacheIsUsedAndStaleIsNot(t *testing.T) {
	fresh := &model.RepoFacts{Repo: "a/b", Stars: 7,
		FetchedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)}
	api := &fakeAPI{meta: metaJSON(999, "Go")}
	c := &fakeCache{loaded: fresh}

	got, err := Fetch(context.Background(), api, nil, c, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	if got.Stars != 7 || len(api.gets) != 0 {
		t.Fatalf("a fresh cache was ignored: stars=%d calls=%v", got.Stars, api.gets)
	}

	c.loaded = &model.RepoFacts{Repo: "a/b", Stars: 7,
		FetchedAt: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)}
	got, err = Fetch(context.Background(), api, nil, c, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	if got.Stars != 999 {
		t.Fatalf("a %d-day-old cache was reused", CacheDays+1)
	}
	if c.saved == nil || c.saved.FetchedAt == "" {
		t.Error("the refetch was not cached")
	}
}

func TestRefreshIgnoresTheCache(t *testing.T) {
	c := &fakeCache{loaded: &model.RepoFacts{Repo: "a/b", Stars: 7,
		FetchedAt: now.Format(time.RFC3339)}}
	api := &fakeAPI{meta: metaJSON(999, "Go")}
	o := opts()
	o.Refresh = true
	got, err := Fetch(context.Background(), api, nil, c, "a/b", o)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stars != 999 {
		t.Fatal("--refresh reused the cache")
	}
}

// TestACacheWithNoTimestampIsNotTrusted: a hand-edited or pre-versioning file
// would otherwise be treated as fresh forever.
func TestACacheWithNoTimestampIsNotTrusted(t *testing.T) {
	c := &fakeCache{loaded: &model.RepoFacts{Repo: "a/b", Stars: 7}}
	api := &fakeAPI{meta: metaJSON(999, "Go")}
	got, err := Fetch(context.Background(), api, nil, c, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	if got.Stars != 999 {
		t.Fatal("a cache entry with no fetched_at was trusted")
	}
}

func TestHasTests(t *testing.T) {
	listing := func(names ...string) string {
		var es []map[string]string
		for _, n := range names {
			es = append(es, map[string]string{"name": n})
		}
		b, _ := json.Marshal(es)
		return string(b)
	}
	tests := []struct {
		name    string
		entries map[string]string
		want    bool
	}{
		{"a tests directory", map[string]string{
			"repos/a/b/contents/": listing("src", "tests", "README.md")}, true},
		{"a marker file", map[string]string{
			"repos/a/b/contents/": listing("src", "tox.ini")}, true},
		{"workflows as a fallback", map[string]string{
			"repos/a/b/contents/":                  listing("src"),
			"repos/a/b/contents/.github/workflows": listing("ci.yml")}, true},
		{"nothing", map[string]string{
			"repos/a/b/contents/": listing("src", "README.md")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{meta: metaJSON(1, "Go"), entries: tt.entries}
			f, err := Fetch(context.Background(), api, nil, nil, "a/b", opts())
			if err != nil {
				t.Fatal(err)
			}
			if f.HasTests != tt.want {
				t.Errorf("= %v, want %v", f.HasTests, tt.want)
			}
		})
	}
}

func TestMergedFirstTimePR(t *testing.T) {
	tests := []struct {
		name  string
		assoc []string
		want  bool
	}{
		{"an outside contributor merged", []string{"MEMBER", "FIRST_TIME_CONTRIBUTOR"}, true},
		{"a plain contributor counts", []string{"CONTRIBUTOR"}, true},
		{"only insiders", []string{"MEMBER", "OWNER", "COLLABORATOR"}, false},
		{"nothing merged", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nodes []map[string]string
			for _, a := range tt.assoc {
				nodes = append(nodes, map[string]string{"authorAssociation": a})
			}
			api := &fakeAPI{meta: metaJSON(1, "Go"),
				gql: map[string]any{"search": map[string]any{"nodes": nodes}}}
			f, err := Fetch(context.Background(), api, nil, nil, "a/b", opts())
			if err != nil {
				t.Fatal(err)
			}
			if f.MergedFirstTimePR90d != tt.want {
				t.Errorf("= %v, want %v", f.MergedFirstTimePR90d, tt.want)
			}
		})
	}
}

// TestASearchFailureIsNotAVerdict: an API error must not read as "this project
// never merges outside PRs", which would reject it on a network blip.
func TestASearchFailureIsNotAVerdict(t *testing.T) {
	api := &fakeAPI{meta: metaJSON(1, "Go"), gqlErr: errors.New("rate limited")}
	f, err := Fetch(context.Background(), api, nil, nil, "a/b", opts())
	if err != nil {
		t.Fatal(err)
	}
	if f.MergedFirstTimePR90d {
		t.Error("a failed search returned true")
	}
	// It is false, which the scorer treats as a soft signal rather than a
	// rejection; the point is that Fetch itself does not fail.
}

func TestMissingRepoMetadataIsAnError(t *testing.T) {
	if _, err := Fetch(context.Background(), &fakeAPI{}, nil, nil, "a/b", opts()); !errors.Is(err, ErrRepoFacts) {
		t.Fatalf("err = %v", err)
	}
}

func TestExcerptsAreBoundedAroundTheKeyword(t *testing.T) {
	long := strings.Repeat("x", 100000) + " we ban AI-generated code " + strings.Repeat("y", 100000)
	got := excerpts(long, 600)
	if len(got) > 2000 {
		t.Fatalf("excerpt is %d bytes; a 200 kB guide must not become the prompt", len(got))
	}
	if !strings.Contains(got, "AI-generated") {
		t.Error("the excerpt lost the keyword it was built around")
	}
	if excerpts("nothing relevant here", 600) != "" {
		t.Error("excerpts invented content with no match")
	}
}
