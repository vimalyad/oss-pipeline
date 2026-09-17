package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testNotifier(t *testing.T, srv string) (*Notifier, *[]*http.Request) {
	t.Helper()
	d := t.TempDir()
	n := New(d, Config{Server: srv, Topic: "topic-secret",
		CommandTopic: "cmd-secret", Location: time.UTC})
	var got []*http.Request
	return n, &got
}

func recordingServer(t *testing.T, got *[]*http.Request, bodies *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		*got = append(*got, r)
		*bodies = append(*bodies, string(buf))
		w.WriteHeader(200)
	}))
}

func TestSendPostsTitleAndPriority(t *testing.T) {
	var got []*http.Request
	var bodies []string
	srv := recordingServer(t, &got, &bodies)
	defer srv.Close()
	n, _ := testNotifier(t, srv.URL)

	sent, err := n.Send(context.Background(),
		Merged("a__b__1", "a/b", 1, "https://github.com/a/b/pull/9"))
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if len(got) != 1 {
		t.Fatal("no request made")
	}
	r := got[0]
	if !strings.Contains(r.Header.Get("Title"), "Merged") {
		t.Errorf("Title = %q", r.Header.Get("Title"))
	}
	if r.Header.Get("Priority") != "4" {
		t.Errorf("Priority = %q", r.Header.Get("Priority"))
	}
	if r.Header.Get("Click") == "" {
		t.Error("tapping the notification should open the PR")
	}
	if !strings.Contains(r.URL.Path, "topic-secret") {
		t.Errorf("posted to %q", r.URL.Path)
	}
}

// The hourly watcher would otherwise announce the same failing check about a
// hundred times.
func TestDedupeSuppressesRepeats(t *testing.T) {
	var got []*http.Request
	var bodies []string
	srv := recordingServer(t, &got, &bodies)
	defer srv.Close()
	n, _ := testNotifier(t, srv.URL)

	e := CIFailed("a__b__1", "a/b", 1, "u", "test (3.12)", "abc123def", "assert failed")
	for i := 0; i < 5; i++ {
		n.Send(context.Background(), e)
	}
	if len(got) != 1 {
		t.Fatalf("sent %d times, want 1", len(got))
	}

	// A new head commit is a genuinely new event and must notify again.
	e2 := CIFailed("a__b__1", "a/b", 1, "u", "test (3.12)", "999fff000", "assert failed")
	n.Send(context.Background(), e2)
	if len(got) != 2 {
		t.Fatalf("a new commit should re-notify; sent %d", len(got))
	}
}

// Commands must land on a different topic from notifications, so a
// screenshotted notification leaks read access and nothing more.
func TestActionButtonsUseTheSeparateCommandTopic(t *testing.T) {
	var got []*http.Request
	var bodies []string
	srv := recordingServer(t, &got, &bodies)
	defer srv.Close()
	n, _ := testNotifier(t, srv.URL)

	n.Send(context.Background(), ApprovalNeeded("a__b__1", "a/b", 1,
		"https://github.com/a/b/issues/1", "Title", "do x", "we would y", "z"))
	actions := got[0].Header.Get("Actions")
	if !strings.Contains(actions, "cmd-secret") {
		t.Fatalf("actions must post to the command topic: %q", actions)
	}
	if strings.Contains(actions, "/topic-secret,") {
		t.Error("actions must not post to the notification topic")
	}
	for _, want := range []string{"Ship it", "Skip", "approve", "reject"} {
		if !strings.Contains(actions, want) {
			t.Errorf("missing %q in %q", want, actions)
		}
	}
}

// The user approves a reply from a lock screen, so the text they are sending
// has to be in front of them.
func TestReplyNotificationCarriesTheFullDraft(t *testing.T) {
	draft := "Thanks — pushed a fix in e9c94a8 that guards the empty case."
	e := ReplyNeeded("a__b__1", "a/b", 1, "u", "maintainer", draft, 0)
	if !strings.Contains(e.Body, draft) {
		t.Fatal("the draft must be readable before it is sent")
	}
	var verbs []string
	for _, a := range e.Actions {
		verbs = append(verbs, a.Verb)
	}
	if !contains(verbs, "reply-post") {
		t.Errorf("no way to send it: %v", verbs)
	}
}

func TestQuietHoursHoldLowPriorityButNotHigh(t *testing.T) {
	var got []*http.Request
	var bodies []string
	srv := recordingServer(t, &got, &bodies)
	defer srv.Close()
	n, _ := testNotifier(t, srv.URL)
	n.Cfg.QuietStart, n.Cfg.QuietEnd = "23:00", "08:00"
	n.Now = func() time.Time { return time.Date(2026, 9, 17, 2, 0, 0, 0, time.UTC) }

	if sent, _ := n.Send(context.Background(), CIFailed("s", "a/b", 1, "u", "c", "sha", "w")); sent {
		t.Error("a default-priority push should wait for the digest at 2am")
	}
	if sent, _ := n.Send(context.Background(), Merged("s", "a/b", 1, "u")); !sent {
		t.Error("a merge is worth waking up for")
	}
}

func TestQuietWindowCrossingMidnight(t *testing.T) {
	c := Config{QuietStart: "23:00", QuietEnd: "08:00", Location: time.UTC}
	for _, tc := range []struct {
		h    int
		want bool
	}{{23, true}, {2, true}, {7, true}, {8, false}, {12, false}, {22, false}} {
		got := c.Quiet(time.Date(2026, 1, 1, tc.h, 0, 0, 0, time.UTC))
		if got != tc.want {
			t.Errorf("hour %d: quiet=%v want %v", tc.h, got, tc.want)
		}
	}
}

// Without a topic the pipeline must still run. Notifications are an
// improvement, not a dependency.
func TestUnconfiguredIsNotAnError(t *testing.T) {
	n := New(t.TempDir(), Config{})
	sent, err := n.Send(context.Background(), Merged("s", "a/b", 1, "u"))
	if err != nil || sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
}

// needs_approval must not push: nothing is waiting on it, and one open
// question at a time is the point.
func TestApprovalWaitsForTheDigest(t *testing.T) {
	if Immediate(KindNeedsApproval) {
		t.Error("needs_approval should be digest-only")
	}
	for _, k := range []string{KindPRMerged, KindMaintainerReplied, KindInternalError} {
		if !Immediate(k) {
			t.Errorf("%s should push immediately", k)
		}
	}
}

func TestSeenStorePersistsAndPrunes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notified.json")
	s := NewSeenStore(path)
	now := time.Now()
	s.Mark("a", now)
	s.Mark("b", now.AddDate(0, 0, -60))

	if !NewSeenStore(path).Has("a") {
		t.Fatal("did not persist")
	}
	s2 := NewSeenStore(path)
	s2.Prune(now, 30*24*time.Hour)
	if s2.Has("b") {
		t.Error("old entry should be pruned")
	}
	if !s2.Has("a") {
		t.Error("recent entry should survive")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
