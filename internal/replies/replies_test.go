package replies

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vimalyad/osspipeline/internal/model"
)

type fakeDrafter struct {
	out    string
	err    error
	prompt string
	calls  int
}

func (f *fakeDrafter) Judge(_ context.Context, prompt string) (string, error) {
	f.calls++
	f.prompt = prompt
	return f.out, f.err
}

type fakeCommenter struct {
	posted []string
	err    error
}

func (f *fakeCommenter) Comment(_ context.Context, _ string, _ int, body string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.posted = append(f.posted, body)
	return "https://github.com/a/b/pull/1#issuecomment-1", nil
}

func cand(items ...map[string]any) *model.Candidate {
	n := 4455
	return &model.Candidate{
		Repo: "kornia/kornia", Issue: 4201, Title: "a title", PRNumber: &n,
		QueuedReplies: items,
	}
}

func TestDraftStoresTheText(t *testing.T) {
	c := cand(map[string]any{"body": "why not use X?"})
	d := &fakeDrafter{out: "  Because X allocates per call; the diff uses Y.  "}

	got, err := Draft(context.Background(), d, c, 0, Context{Commits: "abc fix", Diff: "--- a\n+++ b"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Because X allocates per call; the diff uses Y." {
		t.Errorf("draft = %q (untrimmed?)", got)
	}
	if c.QueuedReplies[0]["draft"] != got {
		t.Error("the draft was not stored on the candidate")
	}
	if _, ok := c.QueuedReplies[0]["draft_rejected"]; ok {
		t.Error("a clean draft was flagged")
	}
}

// TestTheDiffReachesThePrompt is not a formatting check. A draft written from
// commit subjects alone once announced three fixes as "not yet done" when the
// diff already contained all three -- three false statements to a maintainer,
// under the user's name, had it been posted.
func TestTheDiffReachesThePrompt(t *testing.T) {
	c := cand(map[string]any{"body": "is the null check in?"})
	d := &fakeDrafter{out: "ok"}
	diff := "+++ b/kornia/geometry/homography.py\n+    if H is None:\n"

	if _, err := Draft(context.Background(), d, c, 0, Context{Diff: diff, Commits: "a1 docs"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{diff, "a1 docs", "kornia/kornia", "4455", "is the null check in?"} {
		if !strings.Contains(d.prompt, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
}

func TestMissingContextIsStatedNotBlank(t *testing.T) {
	// "(diff unavailable)" tells the model it is working blind. An empty
	// string reads as "there is no diff", which is a different claim.
	c := cand(map[string]any{"body": "?"})
	d := &fakeDrafter{out: "ok"}
	if _, err := Draft(context.Background(), d, c, 0, Context{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(diff unavailable)", "(nothing pushed since)"} {
		if !strings.Contains(d.prompt, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
}

// TestAForbiddenDraftIsKeptButUnpostable: v1 replaced the whole draft with a
// marker, leaving the author a blank page and no sight of what was written.
func TestAForbiddenDraftIsKeptButUnpostable(t *testing.T) {
	c := cand(map[string]any{"body": "?"})
	d := &fakeDrafter{out: "I was unable to reproduce this; the assistant suggested Y."}

	if _, err := Draft(context.Background(), d, c, 0, Context{}); err != nil {
		t.Fatal(err)
	}
	item := c.QueuedReplies[0]
	if item["draft"] != d.out {
		t.Error("the text was discarded, so the author cannot edit it")
	}
	flags := fromAny(item["draft_rejected"])
	if len(flags) == 0 {
		t.Fatal("a draft mentioning an assistant was not flagged")
	}
	ok, why := Usable(item)
	if ok {
		t.Fatal("a flagged draft is postable")
	}
	if !strings.Contains(why, "edit it") {
		t.Errorf("why = %q; the author needs to know what to do", why)
	}

	cm := &fakeCommenter{}
	if _, err := Post(context.Background(), cm, c, 0); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("Post err = %v, want ErrNoDraft", err)
	}
	if len(cm.posted) != 0 {
		t.Fatal("a flagged draft reached GitHub")
	}
}

// TestPostRechecksTheTextItIsAboutToSend: the draft on disk is hand-editable by
// design, so the check that ran when it was written says nothing about what is
// there now.
func TestPostRechecksTheTextItIsAboutToSend(t *testing.T) {
	c := cand(map[string]any{"body": "?", "draft": "clean text"})
	// A human edits the file between drafting and posting.
	c.QueuedReplies[0]["draft"] = "Claude rewrote this bit."

	cm := &fakeCommenter{}
	_, err := Post(context.Background(), cm, c, 0)
	if !errors.Is(err, ErrNoDraft) {
		t.Fatalf("err = %v, want ErrNoDraft", err)
	}
	if len(cm.posted) != 0 {
		t.Fatal("edited forbidden text was posted")
	}
	if len(fromAny(c.QueuedReplies[0]["draft_rejected"])) == 0 {
		t.Error("the re-check did not record what it found")
	}
}

func TestPostMarksItemPosted(t *testing.T) {
	c := cand(map[string]any{"body": "?", "draft": "The diff already does that, in a1b2c3d."})
	cm := &fakeCommenter{}

	url, err := Post(context.Background(), cm, c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cm.posted) != 1 {
		t.Fatalf("posted %d comments", len(cm.posted))
	}
	if b, _ := c.QueuedReplies[0]["posted"].(bool); !b {
		t.Error("the item was not marked posted, so it would be sent again")
	}
	if c.QueuedReplies[0]["posted_url"] != url {
		t.Error("the posted URL was not recorded")
	}
	if len(Pending(c)) != 0 {
		t.Error("a posted item is still pending")
	}
}

func TestPostingTwiceIsRefused(t *testing.T) {
	c := cand(map[string]any{"body": "?", "draft": "text", "posted": true})
	cm := &fakeCommenter{}
	if _, err := Post(context.Background(), cm, c, 0); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("err = %v", err)
	}
	if len(cm.posted) != 0 {
		t.Fatal("a maintainer got the same reply twice")
	}
}

func TestDraftAllSkipsWhatIsAlreadyDrafted(t *testing.T) {
	c := cand(
		map[string]any{"body": "one", "draft": "already written"},
		map[string]any{"body": "two"},
		map[string]any{"body": "three", "posted": true},
	)
	d := &fakeDrafter{out: "new text"}

	n, err := DraftAll(context.Background(), d, c, Context{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || d.calls != 1 {
		t.Fatalf("wrote %d drafts in %d calls, want 1 and 1", n, d.calls)
	}
	if c.QueuedReplies[0]["draft"] != "already written" {
		t.Error("an existing draft was overwritten")
	}
	if _, ok := c.QueuedReplies[2]["draft"]; ok {
		t.Error("a posted item was redrafted")
	}
}

// TestOneBadItemDoesNotBlockTheRest: a maintainer waiting on item three should
// not go unanswered because item one upset the model.
func TestOneBadItemDoesNotBlockTheRest(t *testing.T) {
	c := cand(map[string]any{"body": "one"}, map[string]any{"body": "two"})
	calls := 0
	d := &failEveryOther{n: &calls}

	n, err := DraftAll(context.Background(), d, c, Context{})
	if err == nil {
		t.Fatal("the failure was not reported")
	}
	if n != 1 {
		t.Fatalf("wrote %d drafts, want 1", n)
	}
	if c.QueuedReplies[1]["draft"] != "second" {
		t.Error("the second item was skipped after the first failed")
	}
}

type failEveryOther struct{ n *int }

func (f *failEveryOther) Judge(context.Context, string) (string, error) {
	*f.n++
	if *f.n == 1 {
		return "", errors.New("model refused")
	}
	return "second", nil
}

func TestAnEmptyDraftIsAnError(t *testing.T) {
	// Storing "" would look like a drafted item and silently never be retried.
	c := cand(map[string]any{"body": "?"})
	if _, err := Draft(context.Background(), &fakeDrafter{out: "   "}, c, 0, Context{}); err == nil {
		t.Fatal("an empty draft was accepted")
	}
	if _, ok := c.QueuedReplies[0]["draft"]; ok {
		t.Error("an empty draft was stored")
	}
}

// TestRejectionFlagsSurviveAReload: JSON round-trips a []string back as []any,
// so code that assumed otherwise would fail only on the second run.
func TestRejectionFlagsSurviveAReload(t *testing.T) {
	item := map[string]any{"draft": "text", "draft_rejected": []any{"ai", "claude"}}
	if ok, why := Usable(item); ok || !strings.Contains(why, "ai") {
		t.Fatalf("ok=%v why=%q", ok, why)
	}
}

func TestOutOfRangeIndex(t *testing.T) {
	c := cand(map[string]any{"body": "?"})
	if _, err := Draft(context.Background(), &fakeDrafter{}, c, 5, Context{}); err == nil {
		t.Error("Draft accepted an out-of-range index")
	}
	if _, err := Post(context.Background(), &fakeCommenter{}, c, -1); err == nil {
		t.Error("Post accepted a negative index")
	}
}
