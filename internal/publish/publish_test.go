package publish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	args  []string
	stdin string
}

type fakeAPI struct {
	calls []call
	// reply is keyed by a substring of the joined args.
	reply map[string]string
	fail  map[string]error
}

func (f *fakeAPI) RESTRaw(_ context.Context, args []string, stdin string) (string, error) {
	f.calls = append(f.calls, call{args, stdin})
	joined := strings.Join(args, " ")
	for k, err := range f.fail {
		if strings.Contains(joined, k) {
			return "", err
		}
	}
	for k, v := range f.reply {
		if strings.Contains(joined, k) {
			return v, nil
		}
	}
	return "", nil
}

const scopeHeader = "HTTP/2.0 200 OK\r\nX-OAuth-Scopes: public_repo, gist, read:org\r\n\r\n{}"
const noGistHeader = "HTTP/2.0 200 OK\r\nX-OAuth-Scopes: public_repo, read:org\r\n\r\n{}"

func TestHasGistScopeReadsTheHeader(t *testing.T) {
	tests := []struct {
		name, resp string
		want       bool
	}{
		{"scope present", scopeHeader, true},
		{"scope absent", noGistHeader, false},
		{"header missing entirely", "HTTP/2.0 200 OK\r\n\r\n{}", false},
		{"lowercase header name", "x-oauth-scopes: gist\r\n\r\n{}", true},
		// "gist" must not match "gist:read" or a substring of another scope.
		{"similar scope is not the scope", "X-OAuth-Scopes: gistsomething, repo\r\n\r\n{}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{reply: map[string]string{"user": tt.resp}}
			got, err := HasGistScope(context.Background(), api)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("= %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAnUnreachableAPIIsNotAScopeProblem: reporting "scope missing" for a
// dropped connection sends the reader to the wrong GitHub settings page.
func TestAnUnreachableAPIIsNotAScopeProblem(t *testing.T) {
	api := &fakeAPI{fail: map[string]error{"user": errors.New("dial tcp: connection refused")}}
	_, err := HasGistScope(context.Background(), api)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrScopeMissing) {
		t.Error("a network failure was reported as a missing scope")
	}

	_, perr := Publish(context.Background(), api, Store{Root: t.TempDir()}, "body")
	if !errors.Is(perr, ErrUnreachable) {
		t.Fatalf("Publish err = %v, want ErrUnreachable", perr)
	}
}

func TestMissingScopeStopsBeforeAnyWrite(t *testing.T) {
	api := &fakeAPI{reply: map[string]string{"user": noGistHeader}}
	if _, err := Publish(context.Background(), api, Store{Root: t.TempDir()}, "body"); !errors.Is(err, ErrScopeMissing) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range api.calls {
		if strings.Contains(strings.Join(c.args, " "), "POST") {
			t.Fatal("attempted to create a gist without the scope")
		}
	}
}

func TestFirstRunCreatesASecretGist(t *testing.T) {
	dir := t.TempDir()
	api := &fakeAPI{reply: map[string]string{
		"user":       scopeHeader,
		"POST gists": `{"id":"abc123"}`,
	}}
	res, err := Publish(context.Background(), api, Store{Root: dir}, "# page")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.ID != "abc123" || res.URL != "https://gist.github.com/abc123" {
		t.Fatalf("res = %+v", res)
	}
	// "public": false is what makes it a secret gist; a true here publishes
	// the user's contribution tracking to a searchable list.
	var found bool
	for _, c := range api.calls {
		if strings.Contains(strings.Join(c.args, " "), "POST gists") {
			found = true
			if !strings.Contains(c.stdin, `"public":false`) {
				t.Errorf("gist was not created secret: %s", c.stdin)
			}
			if !strings.Contains(c.stdin, "# page") {
				t.Error("the page body did not reach the payload")
			}
		}
	}
	if !found {
		t.Fatal("no create call")
	}
	if got := (Store{Root: dir}).ID(); got != "abc123" {
		t.Errorf("id was not recorded: %q", got)
	}
}

func TestSecondRunUpdatesInPlace(t *testing.T) {
	dir := t.TempDir()
	st := Store{Root: dir}
	if err := st.setID("abc123"); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{reply: map[string]string{"user": scopeHeader}}

	res, err := Publish(context.Background(), api, st, "# updated")
	if err != nil {
		t.Fatal(err)
	}
	if res.Created {
		t.Error("an existing gist was recreated, so the URL the user saved changed")
	}
	if res.ID != "abc123" {
		t.Errorf("id = %q", res.ID)
	}
	for _, c := range api.calls {
		if strings.Contains(strings.Join(c.args, " "), "POST gists") {
			t.Fatal("created a second gist instead of updating")
		}
	}
}

// TestAGistDeletedByHandIsRecreated: failing forever on a gist that no longer
// exists would silently stop the only page the user can read from a phone.
func TestAGistDeletedByHandIsRecreated(t *testing.T) {
	dir := t.TempDir()
	st := Store{Root: dir}
	if err := st.setID("gone"); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{
		reply: map[string]string{"user": scopeHeader, "POST gists": `{"id":"new456"}`},
		fail:  map[string]error{"PATCH gists/gone": errors.New("HTTP 404: Not Found")},
	}
	res, err := Publish(context.Background(), api, st, "# page")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.ID != "new456" {
		t.Fatalf("res = %+v", res)
	}
	if got := st.ID(); got != "new456" {
		t.Errorf("the new id was not recorded: %q", got)
	}
}

func TestARevokedScopeDuringUpdateIsNamed(t *testing.T) {
	dir := t.TempDir()
	st := Store{Root: dir}
	_ = st.setID("abc123")
	api := &fakeAPI{
		reply: map[string]string{"user": scopeHeader},
		fail:  map[string]error{"PATCH": errors.New("HTTP 403: Resource not accessible by personal access token")},
	}
	if _, err := Publish(context.Background(), api, st, "b"); !errors.Is(err, ErrScopeMissing) {
		t.Fatalf("err = %v, want ErrScopeMissing", err)
	}
	// The id must survive: the gist still exists, we just cannot write it.
	if st.ID() != "abc123" {
		t.Error("a permissions error discarded the gist id")
	}
}

// TestACreatedGistWeCannotRecordIsReported: silently losing the id means the
// next run creates a second gist, and the one after that a third.
func TestACreatedGistWeCannotRecordIsReported(t *testing.T) {
	dir := t.TempDir()
	// Make state/ a file so the write must fail.
	if err := os.WriteFile(filepath.Join(dir, "state"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{reply: map[string]string{"user": scopeHeader, "POST gists": `{"id":"orphan"}`}}

	res, err := Publish(context.Background(), api, Store{Root: dir}, "b")
	if err == nil {
		t.Fatal("the unrecorded gist was not reported")
	}
	if res.ID != "orphan" {
		t.Error("the id was not returned, so it cannot be recovered by hand")
	}
	if !strings.Contains(err.Error(), "orphan") {
		t.Errorf("err = %v; it must name the gist that now exists", err)
	}
}

func TestMalformedCreateResponse(t *testing.T) {
	api := &fakeAPI{reply: map[string]string{"user": scopeHeader, "POST gists": `not json`}}
	if _, err := Publish(context.Background(), api, Store{Root: t.TempDir()}, "b"); err == nil {
		t.Fatal("a malformed response was accepted")
	}
}

func TestStoreIDOnMissingOrCorruptFile(t *testing.T) {
	dir := t.TempDir()
	st := Store{Root: dir}
	if got := st.ID(); got != "" {
		t.Errorf("missing file: %q", got)
	}
	_ = os.MkdirAll(filepath.Join(dir, "state"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "state", "gist.json"), []byte("{oops"), 0o644)
	if got := st.ID(); got != "" {
		t.Errorf("corrupt file: %q", got)
	}
}
