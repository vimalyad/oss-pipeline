package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeAPI struct {
	thread any
	prs    map[int]any
	prErr  map[int]error
	calls  []int
}

func (f *fakeAPI) GraphQL(_ context.Context, q string, vars map[string]any, v any) error {
	n, _ := vars["number"].(int)
	if strings.Contains(q, "pullRequest(number") {
		f.calls = append(f.calls, n)
		if err, ok := f.prErr[n]; ok {
			return err
		}
		body, ok := f.prs[n]
		if !ok {
			return errors.New("not found")
		}
		b, _ := json.Marshal(map[string]any{
			"repository": map[string]any{"pullRequest": body}})
		return json.Unmarshal(b, v)
	}
	b, _ := json.Marshal(f.thread)
	return json.Unmarshal(b, v)
}

func thread(comments []any, timeline []any) any {
	return map[string]any{
		"repository": map[string]any{
			"nameWithOwner":   "kornia/kornia",
			"stargazerCount":  11346,
			"primaryLanguage": map[string]any{"name": "Python"},
			"repositoryTopics": map[string]any{"nodes": []any{
				map[string]any{"topic": map[string]any{"name": "Computer-Vision"}}}},
			"issue": map[string]any{
				"number": 4201, "title": "MPS svd fails", "url": "https://x",
				"body": "batched svd returns garbage", "createdAt": "2026-01-01T00:00:00Z",
				"author": map[string]any{"login": "reporter"}, "authorAssociation": "NONE",
				"labels":        map[string]any{"nodes": []any{map[string]any{"name": "bug"}}},
				"comments":      map[string]any{"totalCount": len(comments), "nodes": comments},
				"timelineItems": map[string]any{"nodes": timeline},
			},
		},
	}
}

func comment(login, assoc, body string) any {
	return map[string]any{
		"url": "https://c", "createdAt": "2026-02-01T00:00:00Z", "body": body,
		"authorAssociation": assoc, "author": map[string]any{"login": login},
		"reactions": map[string]any{"totalCount": 3},
	}
}

func crossRef(number int) any {
	return map[string]any{
		"__typename": "CrossReferencedEvent", "createdAt": "2026-02-02T00:00:00Z",
		"source": map[string]any{"__typename": "PullRequest", "number": number,
			"url": "https://pr", "state": "OPEN"},
	}
}

// TestAuthorAssociationSurvivesRendering is the distinction the whole package
// protects. An OWNER statement is the specification; a drive-by NONE comment
// is one stranger's opinion, and collapsing the two is how a pipeline ends up
// implementing a suggestion nobody with authority endorsed.
func TestAuthorAssociationSurvivesRendering(t *testing.T) {
	api := &fakeAPI{thread: thread([]any{
		comment("stranger", "NONE", "just cast it to float64"),
		comment("ducha-aiki", "OWNER", "document the limit instead"),
	}, nil)}

	p, err := Harvest(context.Background(), api, "kornia/kornia", 4201)
	if err != nil {
		t.Fatal(err)
	}
	out := Render(p, 0)
	if !strings.Contains(out, "comment by stranger [NONE]") {
		t.Errorf("a NONE comment lost its association:\n%s", out)
	}
	if !strings.Contains(out, "comment by ducha-aiki [OWNER]") {
		t.Errorf("an OWNER comment lost its association:\n%s", out)
	}
	if !strings.Contains(out, "opened by reporter [NONE]") {
		t.Error("the issue author's association is missing")
	}
	for _, want := range []string{"kornia/kornia (Python, 11346 stars)", "ISSUE #4201", "LABELS: bug",
		"batched svd returns garbage", "(3 reactions)"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering is missing %q", want)
		}
	}
}

func TestLinkedPRsAreFetchedOncePerNumber(t *testing.T) {
	api := &fakeAPI{
		thread: thread(nil, []any{crossRef(4455), crossRef(4455), crossRef(900)}),
		prs: map[int]any{
			4455: map[string]any{"number": 4455, "state": "MERGED", "additions": 137,
				"deletions": 19, "changedFiles": 5,
				"author": map[string]any{"login": "vimalyad"}, "body": "fixes it"},
			900: map[string]any{"number": 900, "state": "CLOSED"},
		},
	}
	p, err := Harvest(context.Background(), api, "kornia/kornia", 4201)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 2 {
		t.Fatalf("fetched %v; the duplicate cross-reference should cost nothing", api.calls)
	}
	if got := p.LinkedPRNumbers(); len(got) != 2 {
		t.Fatalf("linked = %v", got)
	}
	out := Render(p, 0)
	if !strings.Contains(out, "LINKED PR #4455 (MERGED) by vimalyad +137/-19 across 5 files") {
		t.Errorf("PR header wrong:\n%s", out)
	}
}

// TestAnUnreadablePRIsRecordedNotDropped: a silent omission looks exactly like
// an issue with no linked work, which is the one reading that would let the
// pipeline contest someone's open pull request.
func TestAnUnreadablePRIsRecordedNotDropped(t *testing.T) {
	api := &fakeAPI{
		thread: thread(nil, []any{crossRef(4455)}),
		prErr:  map[int]error{4455: errors.New("HTTP 403: rate limited")},
	}
	p, err := Harvest(context.Background(), api, "kornia/kornia", 4201)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LinkedPRs) != 1 {
		t.Fatalf("the unreadable PR was dropped: %v", p.LinkedPRs)
	}
	if nums := p.LinkedPRNumbers(); len(nums) != 1 || nums[0] != 4455 {
		t.Fatalf("numbers = %v; contest must still know it exists", nums)
	}
	out := Render(p, 0)
	if !strings.Contains(out, "LINKED PR #4455: could not be read") {
		t.Errorf("the gap is invisible in the transcript:\n%s", out)
	}
}

// TestTruncationKeepsBothEnds: the opening states the problem and the last
// comments are where a maintainer settles the approach. Dropping the tail to
// fit would throw away the decision and keep the debate.
func TestTruncationKeepsBothEnds(t *testing.T) {
	var comments []any
	comments = append(comments, comment("a", "NONE", "FIRST_MARKER "+strings.Repeat("x", 200)))
	for i := 0; i < 400; i++ {
		comments = append(comments, comment("b", "NONE", strings.Repeat("y", 200)))
	}
	comments = append(comments, comment("owner", "OWNER", "LAST_MARKER take approach B"))

	api := &fakeAPI{thread: thread(comments, nil)}
	p, err := Harvest(context.Background(), api, "kornia/kornia", 4201)
	if err != nil {
		t.Fatal(err)
	}
	out := Render(p, 4000)
	if len(out) > 4200 {
		t.Fatalf("rendered %d chars for a 4000 budget", len(out))
	}
	if !strings.Contains(out, "FIRST_MARKER") {
		t.Error("the opening was dropped")
	}
	if !strings.Contains(out, "LAST_MARKER") {
		t.Error("the maintainer's final word was dropped, which is the part that decides the patch")
	}
	if !strings.Contains(out, "middle of thread truncated") {
		t.Error("the truncation is not announced, so a reader cannot tell it happened")
	}
}

func TestADeletedAuthorDoesNotLoseTheComment(t *testing.T) {
	api := &fakeAPI{thread: thread([]any{
		map[string]any{"url": "u", "createdAt": "t", "body": "still relevant",
			"authorAssociation": "MEMBER", "author": nil,
			"reactions": map[string]any{"totalCount": 0}},
	}, nil)}
	p, err := Harvest(context.Background(), api, "a/b", 1)
	if err != nil {
		t.Fatal(err)
	}
	out := Render(p, 0)
	if !strings.Contains(out, "still relevant") {
		t.Fatal("a comment from a deleted account was dropped")
	}
	if !strings.Contains(out, "comment by ? [MEMBER]") {
		t.Errorf("the association was lost with the login:\n%s", out)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	api := &fakeAPI{thread: thread([]any{comment("m", "OWNER", "do X")}, nil)}
	p, err := Harvest(context.Background(), api, "kornia/kornia", 4201)
	if err != nil {
		t.Fatal(err)
	}
	path, err := Save(root, "kornia__kornia__4201", p)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "kornia__kornia__4201.raw.json" {
		t.Errorf("path = %s", path)
	}
	// No .tmp file may survive: the next run would read a half-written file.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
	back, err := Load(root, "kornia__kornia__4201")
	if err != nil {
		t.Fatal(err)
	}
	if Render(back, 0) != Render(p, 0) {
		t.Error("the payload did not survive a round trip")
	}
	if back.Repo.Topics == nil || back.Repo.Topics[0] != "computer-vision" {
		t.Errorf("topics = %v", back.Repo.Topics)
	}
}

func TestMissingIssueIsAnError(t *testing.T) {
	api := &fakeAPI{thread: map[string]any{"repository": map[string]any{"issue": nil}}}
	if _, err := Harvest(context.Background(), api, "a/b", 1); !errors.Is(err, ErrHarvest) {
		t.Fatalf("err = %v", err)
	}
}

func TestCrossReferencedIssuesAreNotFetchedAsPRs(t *testing.T) {
	api := &fakeAPI{thread: thread(nil, []any{
		map[string]any{"__typename": "CrossReferencedEvent",
			"source": map[string]any{"__typename": "Issue", "number": 77}},
	})}
	if _, err := Harvest(context.Background(), api, "a/b", 1); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("fetched %v; a cross-referenced issue is not a pull request", api.calls)
	}
}
