package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/cilog"
	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/notify"
)

// --- fakes ---------------------------------------------------------------

type fakeAPI struct {
	resp string
	err  error
	vars map[string]any
}

func (f *fakeAPI) GraphQL(_ context.Context, _ string, vars map[string]any, v any) error {
	f.vars = vars
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.resp), v)
}

type fakeChecks struct {
	by  map[string]cilog.Verdict
	err error
}

func (f *fakeChecks) Classify(_ context.Context, name, _ string) (cilog.Verdict, error) {
	if f.err != nil {
		return cilog.Verdict{}, f.err
	}
	if v, ok := f.by[name]; ok {
		return v, nil
	}
	return cilog.Verdict{Class: cilog.ClassReal, Why: "a test failed"}, nil
}

type fakeFeedback struct {
	by  map[string]Verdict
	err error
}

func (f *fakeFeedback) Classify(_ context.Context, items []Item) (map[string]Verdict, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]Verdict{}
	for _, it := range items {
		if v, ok := f.by[it.ID]; ok {
			out[it.ID] = v
		}
	}
	return out, nil
}

type fakeStore struct{ saved int }

func (f *fakeStore) Save(*model.Candidate) (string, error) { f.saved++; return "", nil }

type fakeFixer struct {
	pushed bool
	err    error
	calls  int
	items  []Item
}

func (f *fakeFixer) Fix(_ context.Context, _ *model.Candidate, items []Item) (bool, error) {
	f.calls++
	f.items = items
	return f.pushed, f.err
}

func deps(api *fakeAPI) (Deps, *fakeStore, *[]notify.Event, *[]string) {
	st := &fakeStore{}
	var events []notify.Event
	var audits []string
	d := Deps{
		API: api, Store: st,
		Audit:  func(kind, slug, detail string) { audits = append(audits, kind+":"+slug) },
		Notify: func(e notify.Event) { events = append(events, e) },
		Now:    func() time.Time { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) },
	}
	return d, st, &events, &audits
}

func prJSON(body string) string {
	return `{"repository":{"pullRequest":{` + body + `}}}`
}

func candidate(status model.Status) *model.Candidate {
	n := 4455
	return &model.Candidate{
		Repo: "kornia/kornia", Issue: 4201, Status: status,
		PRNumber: &n, PRURL: "https://github.com/kornia/kornia/pull/4455",
	}
}

// --- the transition that was losing merges -------------------------------

// TestEveryLiveStatusCanRecordAMerge is the regression this whole package was
// shaped by. GitHub decides merge and close, not the pipeline, so a merge can
// arrive while a candidate sits in any live state. v1's table had no
// changes_requested -> merged edge, watch attempted it, and the caller
// swallowed the exception: the one merged PR sat unrecorded.
func TestEveryLiveStatusCanRecordAMerge(t *testing.T) {
	for _, from := range model.OpenStatuses {
		for _, to := range []model.Status{model.StatusMerged, model.StatusClosed} {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				merged := to == model.StatusMerged
				state := "OPEN"
				if !merged {
					state = "CLOSED"
				}
				api := &fakeAPI{resp: prJSON(`"number":4455,"state":"` + state +
					`","merged":` + boolStr(merged) + `,"commits":{"nodes":[]}`)}
				d, store, events, audits := deps(api)
				c := candidate(from)

				out, err := Sync(context.Background(), d, c, true)
				if err != nil {
					t.Fatalf("Sync from %s: %v", from, err)
				}
				if c.Status != to {
					t.Fatalf("status = %s, want %s", c.Status, to)
				}
				if out.Status != to {
					t.Errorf("outcome status = %s", out.Status)
				}
				if store.saved == 0 {
					t.Error("a terminal outcome was not persisted")
				}
				if len(*events) == 0 {
					t.Error("a terminal outcome sent no notification")
				}
				if len(*audits) == 0 {
					t.Error("a terminal outcome left no audit entry")
				}
			})
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestAnIllegalTransitionIsLoud: v1 wrapped this in a bare except that logged
// nothing, so a bug in our own ordering looked exactly like a network blip.
func TestAnIllegalTransitionIsLoud(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":true,"commits":{"nodes":[]}`)}
	d, _, events, audits := deps(api)
	// Already merged: merged -> merged is not an edge in the table.
	c := candidate(model.StatusMerged)

	_, err := Sync(context.Background(), d, c, true)
	if !errors.Is(err, model.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	if len(*audits) == 0 || !strings.Contains((*audits)[0], "transition_error") {
		t.Errorf("audits = %v, want a transition_error", *audits)
	}
	if len(*events) == 0 || (*events)[0].Kind != notify.KindInternalError {
		t.Errorf("events = %+v, want an internal_error", *events)
	}
}

// --- checks --------------------------------------------------------------

// TestChecksAreClassifiedByLogNotByName is the correction to v1, which matched
// the check's name against a lint regex. A failing unit test never entered the
// fix loop -- the one failure most worth fixing automatically, since the log
// names the assertion, the file and the line.
func TestChecksAreClassifiedByLogNotByName(t *testing.T) {
	tests := []struct {
		name    string
		check   string
		verdict cilog.Verdict
		err     error
		want    Class
	}{
		{"a failing unit test enters the fix loop", "tests (ubuntu, 3.11)",
			cilog.Verdict{Class: cilog.ClassReal, Why: "assertion failed"}, nil, ClassMechanical},
		{"a failing lint job also does", "lint",
			cilog.Verdict{Class: cilog.ClassReal, Why: "ruff"}, nil, ClassMechanical},
		{"a registry outage does not", "tests (ubuntu, 3.11)",
			cilog.Verdict{Class: cilog.ClassInfra, Why: "registry returned 503"}, nil, ClassInformational},
		{"a mirror job is not a second problem", "collector",
			cilog.Verdict{Class: cilog.ClassMirror, Why: "mirrors other jobs"}, nil, ClassInformational},
		{"an unreadable log goes to a human", "tests",
			cilog.Verdict{Class: cilog.ClassUnknown, Why: "no job id"}, nil, ClassNeedsReply},
		{"an API error goes to a human", "tests",
			cilog.Verdict{}, errors.New("403"), ClassNeedsReply},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &State{HeadSHA: "abc", Failing: []Check{{Name: tt.check, URL: "u"}}}
			cl := &fakeChecks{by: map[string]cilog.Verdict{tt.check: tt.verdict}, err: tt.err}
			ClassifyChecks(context.Background(), cl, st, map[string]bool{})

			if len(st.Items) != 1 {
				t.Fatalf("got %d items", len(st.Items))
			}
			if st.Items[0].Class != tt.want {
				t.Errorf("class = %s, want %s", st.Items[0].Class, tt.want)
			}
			if st.Items[0].Why == "" {
				t.Error("a classification with no reason cannot be justified in a report")
			}
		})
	}
}

func TestAlreadySeenChecksAreNotReported(t *testing.T) {
	st := &State{HeadSHA: "abc", Failing: []Check{{Name: "lint", URL: "u"}}}
	seen := map[string]bool{CheckKey("abc", "lint"): true}
	ClassifyChecks(context.Background(), &fakeChecks{}, st, seen)
	if len(st.Items) != 0 {
		t.Fatalf("re-reported a check already seen on this head: %+v", st.Items)
	}
	// A new head SHA is a different run and must be reported again.
	st2 := &State{HeadSHA: "def", Failing: []Check{{Name: "lint", URL: "u"}}}
	ClassifyChecks(context.Background(), &fakeChecks{}, st2, seen)
	if len(st2.Items) != 1 {
		t.Fatal("a failure on a new head was suppressed")
	}
}

// --- feedback ------------------------------------------------------------

// TestUnclassifiedFeedbackGoesToAHuman: the alternative defaults are to drop a
// maintainer's question silently, or to let an unrecognised label authorise an
// automatic push under the user's name.
func TestUnclassifiedFeedbackGoesToAHuman(t *testing.T) {
	tests := []struct {
		name string
		fc   *fakeFeedback
	}{
		{"classifier returned nothing for the item", &fakeFeedback{by: map[string]Verdict{}}},
		{"classifier errored", &fakeFeedback{err: errors.New("llm down")}},
		{"classifier invented a class", &fakeFeedback{by: map[string]Verdict{
			"c1": {Class: "probably_fine"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &State{Items: []Item{{ID: "c1", Author: "maintainer", Body: "why this approach?"}}}
			_ = ClassifyFeedback(context.Background(), tt.fc, st)
			if st.Items[0].Class != ClassNeedsReply {
				t.Fatalf("class = %q, want needs_reply", st.Items[0].Class)
			}
		})
	}
}

func TestCheckItemsAreNotReclassifiedByTheLLM(t *testing.T) {
	// Checks are decided from their logs; handing them to the feedback
	// classifier as well would let it overrule that.
	st := &State{Items: []Item{
		{ID: "check:abc:lint", Class: ClassMechanical, Why: "ruff"},
		{ID: "c1", Body: "looks good"},
	}}
	fc := &fakeFeedback{by: map[string]Verdict{
		"check:abc:lint": {Class: ClassInformational},
		"c1":             {Class: ClassInformational},
	}}
	_ = ClassifyFeedback(context.Background(), fc, st)
	if st.Items[0].Class != ClassMechanical {
		t.Errorf("a check's class was overwritten: %s", st.Items[0].Class)
	}
	if st.Items[1].Class != ClassInformational {
		t.Errorf("a comment was not classified: %s", st.Items[1].Class)
	}
}

// --- the cycle -----------------------------------------------------------

func TestQueuedFeedbackMovesToChangesRequested(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "commits":{"nodes":[]},
	  "comments":{"nodes":[{"id":"c1","body":"why not use X?","author":{"login":"ducha-aiki"}}]}`)}
	d, _, events, _ := deps(api)
	d.Feedback = &fakeFeedback{by: map[string]Verdict{
		"c1": {Class: ClassNeedsReply, Why: "a question", Action: "explain the choice"}}}
	c := candidate(model.StatusPROpen)

	out, err := Sync(context.Background(), d, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusChangesRequested {
		t.Fatalf("status = %s", c.Status)
	}
	if out.Queued != 1 || len(c.QueuedReplies) != 1 {
		t.Fatalf("queued = %d, replies = %d", out.Queued, len(c.QueuedReplies))
	}
	if len(*events) != 1 || (*events)[0].Kind != notify.KindMaintainerReplied {
		t.Errorf("events = %+v", *events)
	}
	// Nothing may be posted on the user's behalf.
	if strings.Contains(out.Summary, "posted") {
		t.Error("a reply was posted rather than queued")
	}
}

func TestDryRunDoesNotMarkItemsSeen(t *testing.T) {
	body := prJSON(`"number":4455,"state":"OPEN","merged":false,"commits":{"nodes":[]},
	  "comments":{"nodes":[{"id":"c1","body":"hi","author":{"login":"x"}}]}`)
	for _, execute := range []bool{false, true} {
		api := &fakeAPI{resp: body}
		d, _, _, _ := deps(api)
		d.Feedback = &fakeFeedback{by: map[string]Verdict{"c1": {Class: ClassInformational}}}
		c := candidate(model.StatusPROpen)
		if _, err := Sync(context.Background(), d, c, execute); err != nil {
			t.Fatal(err)
		}
		if execute && len(c.WatchSeen) != 1 {
			t.Error("an executed cycle did not record what it saw")
		}
		if !execute && len(c.WatchSeen) != 0 {
			t.Error("a dry run marked items seen, so the next real run would skip them")
		}
	}
}

// TestAFailedFixDoesNotStrandTheCandidate: UPDATING claims a push is in
// flight, and a stuck claim blocks every later cycle.
func TestAFailedFixDoesNotStrandTheCandidate(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "commits":{"nodes":[{"commit":{"oid":"abc","statusCheckRollup":{"state":"FAILURE",
	    "contexts":{"nodes":[{"__typename":"CheckRun","name":"tests","conclusion":"FAILURE",
	      "detailsUrl":"https://github.com/a/b/actions/runs/1/job/2"}]}}}}]}`)}
	d, _, _, audits := deps(api)
	d.Checks = &fakeChecks{}
	d.Fixer = &fakeFixer{err: errors.New("could not reproduce")}
	c := candidate(model.StatusPROpen)

	if _, err := Sync(context.Background(), d, c, true); err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusPROpen {
		t.Fatalf("status = %s, want pr_open after a failed fix", c.Status)
	}
	found := false
	for _, a := range *audits {
		if strings.Contains(a, "ci_fix_failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("a failed fix left no audit entry: %v", *audits)
	}
}

func TestASuccessfulFixReturnsToPROpen(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "commits":{"nodes":[{"commit":{"oid":"abc","statusCheckRollup":{"state":"FAILURE",
	    "contexts":{"nodes":[{"__typename":"CheckRun","name":"lint","conclusion":"FAILURE",
	      "detailsUrl":"https://github.com/a/b/actions/runs/1/job/2"}]}}}}]}`)}
	d, _, _, _ := deps(api)
	d.Checks = &fakeChecks{}
	fx := &fakeFixer{pushed: true}
	d.Fixer = fx
	c := candidate(model.StatusPROpen)

	out, err := Sync(context.Background(), d, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Pushed || c.Status != model.StatusPROpen {
		t.Fatalf("pushed = %v, status = %s", out.Pushed, c.Status)
	}
	if fx.calls != 1 || len(fx.items) != 1 {
		t.Fatalf("fixer called %d times with %d items", fx.calls, len(fx.items))
	}
	if !strings.Contains(out.Summary, "auto-fixed") {
		t.Errorf("summary = %q", out.Summary)
	}
}

func TestADryRunNeverFixes(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "commits":{"nodes":[{"commit":{"oid":"abc","statusCheckRollup":{"state":"FAILURE",
	    "contexts":{"nodes":[{"__typename":"CheckRun","name":"lint","conclusion":"FAILURE",
	      "detailsUrl":"https://github.com/a/b/actions/runs/1/job/2"}]}}}}]}`)}
	d, _, _, _ := deps(api)
	d.Checks = &fakeChecks{}
	fx := &fakeFixer{pushed: true}
	d.Fixer = fx
	c := candidate(model.StatusPROpen)

	if _, err := Sync(context.Background(), d, c, false); err != nil {
		t.Fatal(err)
	}
	if fx.calls != 0 {
		t.Fatal("a dry run pushed a fix")
	}
}

// TestActionRequiredIsNotAFailure: GitHub holds workflow runs from first-time
// contributors until a maintainer approves them. Treating that as red would
// have the pipeline push at it repeatedly, in public.
func TestActionRequiredIsNotAFailure(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "commits":{"nodes":[{"commit":{"oid":"abc","statusCheckRollup":{"state":"PENDING",
	    "contexts":{"nodes":[{"__typename":"CheckRun","name":"tests","conclusion":"ACTION_REQUIRED",
	      "detailsUrl":"u"}]}}}}]}`)}
	d, _, _, _ := deps(api)
	d.Checks = &fakeChecks{}
	fx := &fakeFixer{pushed: true}
	d.Fixer = fx
	c := candidate(model.StatusPROpen)

	out, err := Sync(context.Background(), d, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if fx.calls != 0 {
		t.Fatal("pushed at a check that is waiting on a maintainer")
	}
	if len(out.State.Failing) != 0 || len(out.State.Awaiting) != 1 {
		t.Fatalf("failing=%d awaiting=%d", len(out.State.Failing), len(out.State.Awaiting))
	}
	if !strings.Contains(out.Summary, "awaiting maintainer approval") {
		t.Errorf("summary = %q; the human needs to know why nothing moved", out.Summary)
	}
}

func TestStaleAfterSilence(t *testing.T) {
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "updatedAt":"` + old + `","commits":{"nodes":[]}`)}
	d, _, _, _ := deps(api)
	d.StaleAfterDays = 21
	c := candidate(model.StatusPROpen)

	if _, err := Sync(context.Background(), d, c, true); err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusStale {
		t.Fatalf("status = %s, want stale", c.Status)
	}
	// A stale PR is still open on GitHub, so it must still count against the
	// open-PR cap.
	found := false
	for _, s := range model.OpenStatuses {
		if s == model.StatusStale {
			found = true
		}
	}
	if !found {
		t.Error("stale is not in OpenStatuses, so a stale PR escapes the cap")
	}
}

func TestFreshActivityIsNotStale(t *testing.T) {
	recent := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","merged":false,
	  "updatedAt":"` + recent + `","commits":{"nodes":[]}`)}
	d, _, _, _ := deps(api)
	d.StaleAfterDays = 21
	c := candidate(model.StatusPROpen)
	if _, err := Sync(context.Background(), d, c, true); err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusPROpen {
		t.Fatalf("status = %s", c.Status)
	}
}

func TestNoPRNumberIsNotAnError(t *testing.T) {
	c := &model.Candidate{Repo: "a/b", Issue: 1, Status: model.StatusApproved}
	out, err := Sync(context.Background(), Deps{}, c, true)
	if err != nil || out.Summary != "no PR" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestPollPassesTheRightVariables(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","commits":{"nodes":[]}`)}
	c := candidate(model.StatusPROpen)
	if _, err := Poll(context.Background(), api, c); err != nil {
		t.Fatal(err)
	}
	if api.vars["owner"] != "kornia" || api.vars["name"] != "kornia" || api.vars["number"] != 4455 {
		t.Fatalf("vars = %+v", api.vars)
	}
}

// TestADeletedAuthorDoesNotLoseTheComment: GitHub returns a null author for a
// deleted account, and dropping the comment would be worse than not knowing
// who wrote it.
func TestADeletedAuthorDoesNotLoseTheComment(t *testing.T) {
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","commits":{"nodes":[]},
	  "comments":{"nodes":[{"id":"c1","body":"still relevant","author":null}]}`)}
	c := candidate(model.StatusPROpen)
	st, err := Poll(context.Background(), api, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Items) != 1 || st.Items[0].Author != "?" {
		t.Fatalf("items = %+v", st.Items)
	}
}

func TestEmptyReviewBodiesAreSkippedButInlineCommentsAreNot(t *testing.T) {
	// An APPROVED review with no body carries no information; its inline
	// comments still do.
	api := &fakeAPI{resp: prJSON(`"number":4455,"state":"OPEN","commits":{"nodes":[]},
	  "reviews":{"nodes":[{"id":"r1","state":"APPROVED","body":"  ","author":{"login":"m"},
	    "comments":{"nodes":[{"id":"rc1","path":"a.py","line":3,"body":"nit","url":"u"}]}}]}`)}
	c := candidate(model.StatusPROpen)
	st, err := Poll(context.Background(), api, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Items) != 1 || st.Items[0].ID != "rc1" {
		t.Fatalf("items = %+v", st.Items)
	}
	if st.Items[0].Kind != "inline a.py:3" {
		t.Errorf("kind = %q", st.Items[0].Kind)
	}
}
