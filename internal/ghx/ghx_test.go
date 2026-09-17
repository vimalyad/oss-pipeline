package ghx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGh puts a scripted `gh` on PATH. Each invocation reads the next line of
// a plan file: "0<tab>stdout" succeeds, "1<tab>stderr" fails.
func fakeGh(t *testing.T, steps ...string) {
	t.Helper()
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan")
	os.WriteFile(plan, []byte(strings.Join(steps, "\n")+"\n"), 0o644)
	script := fmt.Sprintf(`#!/bin/sh
n=$(cat %[1]s.n 2>/dev/null || echo 1)
echo $((n+1)) > %[1]s.n
line=$(sed -n "${n}p" %[1]s)
code=${line%%%%	*}
rest=${line#*	}
if [ "$code" = "0" ]; then printf '%%s' "$rest"; exit 0; fi
printf '%%s' "$rest" >&2
exit 1
`, plan)
	bin := filepath.Join(dir, "gh")
	os.WriteFile(bin, []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newTestClient() *Client {
	c := New(os.Environ())
	c.sleep = func(time.Duration) {} // never actually wait in tests
	return c
}

func TestRESTDecodes(t *testing.T) {
	fakeGh(t, "0\t{\"login\":\"someone\"}")
	var v struct{ Login string }
	if err := newTestClient().RESTInto(context.Background(), "user", RESTOptions{}, &v); err != nil {
		t.Fatal(err)
	}
	if v.Login != "someone" {
		t.Fatalf("login = %q", v.Login)
	}
}

// TestRetriesTransientThenSucceeds is the regression test for the outage that
// killed a whole unattended run: a dropped connection is not fatal.
func TestRetriesTransientThenSucceeds(t *testing.T) {
	fakeGh(t,
		"1\terror connecting to api.github.com",
		"1\tTLS handshake timeout",
		"0\t{\"ok\":true}",
	)
	var v struct{ OK bool }
	if err := newTestClient().RESTInto(context.Background(), "x", RESTOptions{}, &v); err != nil {
		t.Fatalf("transient failures must be retried: %v", err)
	}
	if !v.OK {
		t.Fatal("did not reach the successful attempt")
	}
}

func TestSecondaryRateLimitBacksOffThenSucceeds(t *testing.T) {
	fakeGh(t,
		"1\tYou have exceeded a secondary rate limit",
		"0\t{\"ok\":true}",
	)
	var v struct{ OK bool }
	if err := newTestClient().RESTInto(context.Background(), "x", RESTOptions{}, &v); err != nil {
		t.Fatalf("secondary limits must back off, not fail: %v", err)
	}
}

func TestPrimaryRateLimitDoesNotSpin(t *testing.T) {
	fakeGh(t, "1\tAPI rate limit exceeded for user")
	err := newTestClient().RESTInto(context.Background(), "x", RESTOptions{}, nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

// A missing repo must be distinguishable, so discovery can skip it rather
// than abort the sweep.
func TestNotFoundIsItsOwnError(t *testing.T) {
	fakeGh(t, "1\tHTTP 404: Not Found")
	err := newTestClient().RESTInto(context.Background(), "repos/x/y", RESTOptions{}, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if !errors.Is(err, ErrGh) {
		t.Error("ErrNotFound should still classify as a gh error")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	steps := make([]string, MaxAttempts)
	for i := range steps {
		steps[i] = "1\tconnection reset by peer"
	}
	fakeGh(t, steps...)
	err := newTestClient().RESTInto(context.Background(), "x", RESTOptions{}, nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("exhausting retries should be terminal, got %v", err)
	}
}

func TestGraphQLSurfacesErrors(t *testing.T) {
	fakeGh(t, `0	{"data":null,"errors":[{"message":"Could not resolve to a Repository"}]}`)
	err := newTestClient().GraphQL(context.Background(), "query{}", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("graphql errors must surface: %v", err)
	}
}

func TestGraphQLDecodesData(t *testing.T) {
	fakeGh(t, `0	{"data":{"viewer":{"login":"someone"}}}`)
	var v struct {
		Viewer struct{ Login string } `json:"viewer"`
	}
	if err := newTestClient().GraphQL(context.Background(), "q", nil, &v); err != nil {
		t.Fatal(err)
	}
	if v.Viewer.Login != "someone" {
		t.Fatalf("viewer = %+v", v)
	}
}

// gh --paginate emits concatenated JSON documents, not one array, so the
// merge cannot be a single Unmarshal. Exercised directly because the point is
// the parsing, not the subprocess.
func TestMergePagesJoinsConcatenatedDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     int
	}{
		{"newline separated", "[{\"n\":1},{\"n\":2}]\n[{\"n\":3}]", 3},
		{"space separated", `[{"n":1}] [{"n":2}]`, 2},
		{"single page", `[{"n":1},{"n":2}]`, 2},
		{"empty pages", `[] []`, 0},
		{"object pages not arrays", `{"n":1} {"n":2}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := mergePages(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			var items []struct{ N int }
			if err := json.Unmarshal(merged, &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != tc.want {
				t.Fatalf("got %d items, want %d: %s", len(items), tc.want, merged)
			}
		})
	}
}

func TestPaginateEndToEnd(t *testing.T) {
	fakeGh(t, `0	[{"n":1},{"n":2}] [{"n":3}]`)
	var items []struct{ N int }
	err := newTestClient().RESTInto(context.Background(), "x",
		RESTOptions{Paginate: true}, &items)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[2].N != 3 {
		t.Fatalf("merged = %+v, want 3 items", items)
	}
}

func TestContextCancelStopsRetrying(t *testing.T) {
	fakeGh(t, "1\tconnection reset", "1\tconnection reset", "0\t{}")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newTestClient().RESTInto(ctx, "x", RESTOptions{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestBackoffHonoursRetryAfter(t *testing.T) {
	got := backoff(0, "HTTP 403\nRetry-After: 42\n")
	if got != 42*time.Second {
		t.Fatalf("Retry-After ignored: %v", got)
	}
}

func TestBackoffIsJitteredAndCapped(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		d := backoff(attempt, "")
		if d <= 0 || d > 60*time.Second {
			t.Fatalf("attempt %d gave %v; must be within (0, 60s]", attempt, d)
		}
	}
}
