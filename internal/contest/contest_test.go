package contest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/policy"
)

var now = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func st() policy.Staleness {
	return policy.Staleness{
		ActivePRDays: 30, AuthorSilentDays: 45,
		ChangesRequestedDays: 30, CIRedUntouchedDays: 21,
	}
}

func ptr(n int) *int { return &n }

// TestSilenceIsNotEvidenceOfAbandonment is the bias this package exists to
// hold. Opening a competing pull request against live work is the most
// reliably resented thing an outside contributor can do, and the cost lands on
// the user's account.
func TestSilenceIsNotEvidenceOfAbandonment(t *testing.T) {
	// Past the active window, before the silent window, no other signal.
	sig := &model.PRSignal{DaysSinceCommit: ptr(35)}
	got, reasons := ClassifyOne(sig, st())
	if got != model.ContestActivePR {
		t.Fatalf("= %s, want active_pr; ambiguity must resolve to leaving it alone", got)
	}
	if !strings.Contains(strings.Join(reasons, " "), "conservative") {
		t.Errorf("reasons = %v; the default must say it is a default", reasons)
	}
}

func TestClassifyOne(t *testing.T) {
	tests := []struct {
		name    string
		sig     *model.PRSignal
		want    model.Contest
		mention string
	}{
		{"recent commit", &model.PRSignal{DaysSinceCommit: ptr(3)},
			model.ContestActivePR, "author active 3d ago"},
		{"recent comment even with an old commit",
			&model.PRSignal{DaysSinceCommit: ptr(200), DaysSinceAuthorComment: ptr(5)},
			model.ContestActivePR, "author active 5d ago"},
		{"exactly on the active boundary", &model.PRSignal{DaysSinceCommit: ptr(30)},
			model.ContestActivePR, "within 30d window"},
		{"stale label", &model.PRSignal{DaysSinceCommit: ptr(40), HasStaleLabel: true},
			model.ContestStalePR, "stale/abandoned label"},
		{"long silence", &model.PRSignal{DaysSinceCommit: ptr(60)},
			model.ContestStalePR, "no commit for 60d"},
		{"unanswered changes requested",
			&model.PRSignal{DaysSinceCommit: ptr(60), DaysSinceChangesReqested: ptr(40)},
			model.ContestStalePR, "unanswered"},
		{"CI red and untouched",
			&model.PRSignal{DaysSinceCommit: ptr(60), ChecksFailing: true},
			model.ContestStalePR, "CI red and untouched"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := ClassifyOne(tt.sig, st())
			if got != tt.want {
				t.Fatalf("= %s, want %s (%v)", got, tt.want, reasons)
			}
			if !strings.Contains(strings.Join(reasons, " "), tt.mention) {
				t.Errorf("reasons = %v, want one mentioning %q", reasons, tt.mention)
			}
		})
	}
}

// TestAnAnsweredReviewIsNotStalling: an author who replied and is waiting on
// the maintainer is not the one holding things up, and counting that against
// them would contest exactly the pull requests that are working correctly.
func TestAnAnsweredReviewIsNotStalling(t *testing.T) {
	// Changes requested 40 days ago, author replied 35 days ago, no commit
	// since. The reply is more recent than the request.
	sig := &model.PRSignal{
		DaysSinceCommit: ptr(60), DaysSinceChangesReqested: ptr(40),
		DaysSinceAuthorComment: ptr(35),
	}
	_, reasons := ClassifyOne(sig, st())
	for _, r := range reasons {
		if strings.Contains(r, "unanswered") {
			t.Fatalf("an answered review was counted as unanswered: %v", reasons)
		}
	}
	// It is still stale for the silence, which is a different and honest
	// reason.
	if got, _ := ClassifyOne(sig, st()); got != model.ContestStalePR {
		t.Errorf("= %s", got)
	}
}

// TestUnknownDatesAreNeverZero: a missing timestamp read as "0 days ago" makes
// an abandoned pull request look active; read as "very old" it makes a live
// one look abandoned. Only nil is honest, and nil must not trigger a staleness
// reason by itself.
func TestUnknownDatesAreNeverZero(t *testing.T) {
	sig := &model.PRSignal{} // everything unknown
	got, reasons := ClassifyOne(sig, st())
	if got != model.ContestActivePR {
		t.Fatalf("= %s; an unreadable pull request must stay off limits", got)
	}
	for _, r := range reasons {
		if strings.Contains(r, "0d") {
			t.Errorf("an unknown date was reported as 0 days: %v", reasons)
		}
	}
	// And CI failing with an unknown commit date must not become a reason.
	sig2 := &model.PRSignal{ChecksFailing: true}
	if _, rs := ClassifyOne(sig2, st()); strings.Contains(strings.Join(rs, " "), "CI red") {
		t.Errorf("CI staleness claimed with no commit date: %v", rs)
	}
}

func TestDaysSince(t *testing.T) {
	tests := []struct {
		name, iso string
		want      *int
	}{
		{"absent", "", nil},
		{"unparseable", "not-a-date", nil},
		{"three days", now.AddDate(0, 0, -3).Format(time.RFC3339), ptr(3)},
		{"zulu suffix", "2026-09-20T12:00:00Z", ptr(3)},
		// A timestamp in the future is clock skew, not negative age.
		{"future", now.AddDate(0, 0, 2).Format(time.RFC3339), ptr(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daysSince(tt.iso, now)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("= %d, want nil", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("= nil, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("= %d, want %d", *got, *tt.want)
			}
		})
	}
}

func TestHasStaleLabel(t *testing.T) {
	for _, tt := range []struct {
		labels []string
		want   bool
	}{
		{[]string{"bug", "stale"}, true},
		{[]string{"status: inactive"}, true},
		{[]string{"needs-rebase"}, true},
		{[]string{"bug", "enhancement"}, false},
		{nil, false},
	} {
		if got := hasStaleLabel(tt.labels); got != tt.want {
			t.Errorf("%v -> %v, want %v", tt.labels, got, tt.want)
		}
	}
}

// --- whole-issue classification ------------------------------------------

type fakeAPI struct {
	byNumber map[int]any
	errFor   map[int]error
	calls    int
}

func (f *fakeAPI) GraphQL(_ context.Context, _ string, vars map[string]any, v any) error {
	f.calls++
	n, _ := vars["number"].(int)
	if err, ok := f.errFor[n]; ok {
		return err
	}
	body, ok := f.byNumber[n]
	if !ok {
		return errors.New("no such PR")
	}
	b, _ := json.Marshal(map[string]any{
		"repository": map[string]any{"pullRequest": body},
	})
	return json.Unmarshal(b, v)
}

func pr(number int, committedDaysAgo int, extra map[string]any) map[string]any {
	body := map[string]any{
		"number": number, "url": "u", "isDraft": false, "state": "OPEN",
		"author": map[string]any{"login": "someone"},
		"labels": map[string]any{"nodes": []any{}},
		"commits": map[string]any{"nodes": []any{map[string]any{
			"commit": map[string]any{
				"committedDate": now.AddDate(0, 0, -committedDaysAgo).Format(time.RFC3339),
			}}}},
		"comments": map[string]any{"nodes": []any{}},
		"reviews":  map[string]any{"nodes": []any{}},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestNoLinkedPRsNeedsNoCalls(t *testing.T) {
	api := &fakeAPI{}
	got, sig, err := Classify(context.Background(), api, "a/b", nil, st(), now)
	if err != nil || got != model.ContestNoPR || sig != nil {
		t.Fatalf("= %s %v %v", got, sig, err)
	}
	if api.calls != 0 {
		t.Error("an issue with no linked PRs still cost an API call")
	}
}

// TestOneLivePRIsEnough: the issue is off limits if anybody is working on it,
// however many abandoned attempts are also linked.
func TestOneLivePRIsEnough(t *testing.T) {
	api := &fakeAPI{byNumber: map[int]any{
		1: pr(1, 200, nil), // long abandoned
		2: pr(2, 2, nil),   // live
	}}
	got, sig, err := Classify(context.Background(), api, "a/b",
		[]LinkedPR{{Number: 1}, {Number: 2}}, st(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got != model.ContestActivePR {
		t.Fatalf("= %s, want active_pr", got)
	}
	if sig == nil || sig.Number != 2 {
		t.Fatalf("surfaced %v; the live one is the one that matters", sig)
	}
}

// TestAnUnreadablePRMakesTheIssueContested: unknown state is not evidence of
// abandonment, and a rate limit must not hand us someone else's work.
func TestAnUnreadablePRMakesTheIssueContested(t *testing.T) {
	api := &fakeAPI{
		byNumber: map[int]any{1: pr(1, 300, nil)},
		errFor:   map[int]error{2: errors.New("HTTP 403: rate limited")},
	}
	got, _, err := Classify(context.Background(), api, "a/b",
		[]LinkedPR{{Number: 1}, {Number: 2}}, st(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got != model.ContestActivePR {
		t.Fatalf("= %s; an unreadable PR must leave the issue off limits", got)
	}
}

// TestAllStaleSurfacesTheMostAbandoned: the ranking exists to choose between
// abandoned pull requests, never to justify contesting live work.
func TestAllStaleSurfacesTheMostAbandoned(t *testing.T) {
	api := &fakeAPI{byNumber: map[int]any{
		1: pr(1, 60, nil),
		2: pr(2, 400, nil),
		3: pr(3, 90, nil),
	}}
	got, sig, err := Classify(context.Background(), api, "a/b",
		[]LinkedPR{{Number: 1}, {Number: 2}, {Number: 3}}, st(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got != model.ContestStalePR {
		t.Fatalf("= %s", got)
	}
	if sig == nil || sig.Number != 2 {
		t.Fatalf("surfaced %v, want the 400-day-old one", sig)
	}
	if len(sig.Reasons) == 0 {
		t.Error("the surfaced signal carries no reasons, so a report cannot justify it")
	}
}

func TestSignalReadsTheFacts(t *testing.T) {
	api := &fakeAPI{byNumber: map[int]any{7: pr(7, 10, map[string]any{
		"isDraft": true,
		"labels":  map[string]any{"nodes": []any{map[string]any{"Name": "x"}, map[string]any{"name": "Stale"}}},
		"commits": map[string]any{"nodes": []any{map[string]any{
			"commit": map[string]any{
				"committedDate":     now.AddDate(0, 0, -10).Format(time.RFC3339),
				"statusCheckRollup": map[string]any{"state": "FAILURE"},
			}}}},
		"comments": map[string]any{"nodes": []any{
			map[string]any{"createdAt": now.AddDate(0, 0, -20).Format(time.RFC3339),
				"author": map[string]any{"login": "someone"}},
			map[string]any{"createdAt": now.AddDate(0, 0, -1).Format(time.RFC3339),
				"author": map[string]any{"login": "a-maintainer"}},
		}},
		"reviews": map[string]any{"nodes": []any{
			map[string]any{"state": "CHANGES_REQUESTED",
				"createdAt": now.AddDate(0, 0, -15).Format(time.RFC3339)},
			map[string]any{"state": "COMMENTED",
				"createdAt": now.AddDate(0, 0, -2).Format(time.RFC3339)},
		}},
	})}}
	sig, err := Signal(context.Background(), api, "a/b", 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if !sig.IsDraft || !sig.HasStaleLabel || !sig.ChecksFailing {
		t.Fatalf("sig = %+v", sig)
	}
	if sig.DaysSinceCommit == nil || *sig.DaysSinceCommit != 10 {
		t.Errorf("commit age = %v", sig.DaysSinceCommit)
	}
	// Only the author's own comments count; a maintainer's does not prove the
	// author is still around.
	if sig.DaysSinceAuthorComment == nil || *sig.DaysSinceAuthorComment != 20 {
		t.Errorf("author comment age = %v, want 20 (the maintainer's is not the author's)",
			sig.DaysSinceAuthorComment)
	}
	if sig.DaysSinceChangesReqested == nil || *sig.DaysSinceChangesReqested != 15 {
		t.Errorf("changes-requested age = %v; only CHANGES_REQUESTED counts",
			sig.DaysSinceChangesReqested)
	}
}

// TestADeletedAuthorDoesNotMatchEveryComment: an empty author login must not
// make every comment look like the author's, which would keep a long-abandoned
// pull request permanently "active".
func TestADeletedAuthorDoesNotMatchEveryComment(t *testing.T) {
	api := &fakeAPI{byNumber: map[int]any{9: pr(9, 300, map[string]any{
		"author": nil,
		"comments": map[string]any{"nodes": []any{
			map[string]any{"createdAt": now.Format(time.RFC3339), "author": nil},
		}},
	})}}
	sig, err := Signal(context.Background(), api, "a/b", 9, now)
	if err != nil {
		t.Fatal(err)
	}
	if sig.DaysSinceAuthorComment != nil {
		t.Fatalf("a null author matched a null commenter: %v", *sig.DaysSinceAuthorComment)
	}
}
