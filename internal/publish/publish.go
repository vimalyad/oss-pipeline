// Package publish mirrors the status page to a secret gist, so it can be read
// from a phone.
//
// Deliberately not an artifact on claude.ai: that session authenticates as the
// user's work account, under their employer's org, and publishing personal
// open-source tracking there would re-link the two identities the rest of this
// pipeline keeps apart.
//
// A secret gist is unlisted, not private. Anyone holding the URL can read it,
// so nothing sensitive goes in -- only public pull request links and counts
// already visible on GitHub.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Filename is the gist's single file.
	Filename = "oss-pipeline-status.md"
	// Description is stable so the gist stays recognisable in a list.
	Description = "OSS contribution pipeline — status"
)

var (
	// ErrScopeMissing means the token cannot write gists. Actionable, not
	// fatal: everything else in the pipeline still works.
	ErrScopeMissing = errors.New("the token does not carry the `gist` scope")
	// ErrUnreachable means GitHub could not be asked. It says nothing about
	// the token, and conflating the two sends the reader to the wrong page.
	ErrUnreachable = errors.New("could not reach GitHub")
	ErrPublish     = errors.New("publish")
)

// API is the gh surface this package needs.
type API interface {
	// RESTRaw returns stdout, and the error carries stderr, so a caller can
	// tell a 404 from a dropped connection.
	RESTRaw(ctx context.Context, args []string, stdin string) (string, error)
}

// Store remembers which gist we own.
type Store struct{ Root string }

func (s Store) path() string { return filepath.Join(s.Root, "state", "gist.json") }

// ID returns the gist we created, or "" if there is not one yet.
func (s Store) ID() string {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return ""
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.ID
}

func (s Store) setID(id string) error {
	if err := os.MkdirAll(filepath.Dir(s.path()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]string{"id": id}, "", "  ")
	return os.WriteFile(s.path(), append(b, '\n'), 0o644)
}

func (s Store) forget() { _ = os.Remove(s.path()) }

// HasGistScope asks the token what it can do, rather than inferring it from a
// status code.
//
// GitHub answers POST /gists with 404, not 403, when the scope is missing: it
// declines to confirm what exists. Inferring "missing scope" from a 404 is
// guesswork, and the x-oauth-scopes header states it outright.
func HasGistScope(ctx context.Context, api API) (bool, error) {
	out, err := api.RESTRaw(ctx, []string{"api", "-i", "user"}, "")
	if err != nil {
		// A failed call proves nothing about scopes. Reporting "scope
		// missing" for a dropped connection sent the reader to the wrong
		// GitHub settings page.
		return false, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	for _, line := range strings.Split(out, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "x-oauth-scopes") {
			continue
		}
		for _, s := range strings.Split(value, ",") {
			if strings.TrimSpace(s) == "gist" {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

// Result says what happened, so a caller can report it without parsing a
// sentence.
type Result struct {
	ID      string
	URL     string
	Created bool
}

// Publish creates the gist on first run and updates it thereafter.
func Publish(ctx context.Context, api API, st Store, body string) (Result, error) {
	ok, err := HasGistScope(ctx, api)
	if err != nil {
		return Result{}, err // Unreachable propagates; it is not a scope problem.
	}
	if !ok {
		return Result{}, ErrScopeMissing
	}

	if id := st.ID(); id != "" {
		payload, _ := json.Marshal(map[string]any{
			"description": Description,
			"files":       map[string]any{Filename: map[string]string{"content": body}},
		})
		_, err := api.RESTRaw(ctx, []string{"api", "-X", "PATCH", "gists/" + id, "--input", "-"}, string(payload))
		switch {
		case err == nil:
			return Result{ID: id, URL: gistURL(id)}, nil
		case mentions(err, "404"):
			// Deleted by hand. Forget it and create a new one rather than
			// failing forever on a gist that no longer exists.
			st.forget()
		case mentions(err, "scope", "403"):
			return Result{}, fmt.Errorf("%w: %v", ErrScopeMissing, err)
		default:
			return Result{}, fmt.Errorf("%w: update: %v", ErrPublish, err)
		}
	}

	// public:false is GitHub's "secret" gist: unlisted, not access-controlled.
	payload, _ := json.Marshal(map[string]any{
		"description": Description,
		"public":      false,
		"files":       map[string]any{Filename: map[string]string{"content": body}},
	})
	out, err := api.RESTRaw(ctx, []string{"api", "-X", "POST", "gists", "--input", "-"}, string(payload))
	if err != nil {
		// A 404 here almost always means the scope was revoked between the
		// check above and this call.
		if mentions(err, "404", "scope", "403") {
			return Result{}, fmt.Errorf("%w: %v", ErrScopeMissing, err)
		}
		return Result{}, fmt.Errorf("%w: create: %v", ErrPublish, err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		return Result{}, fmt.Errorf("%w: GitHub returned no gist id", ErrPublish)
	}
	if err := st.setID(created.ID); err != nil {
		// The gist exists but we have forgotten it. Say so: the next run
		// would otherwise create a second one, and then a third.
		return Result{ID: created.ID, URL: gistURL(created.ID), Created: true},
			fmt.Errorf("%w: created %s but could not record it: %v", ErrPublish, created.ID, err)
	}
	return Result{ID: created.ID, URL: gistURL(created.ID), Created: true}, nil
}

func gistURL(id string) string { return "https://gist.github.com/" + id }

func mentions(err error, subs ...string) bool {
	s := strings.ToLower(err.Error())
	for _, sub := range subs {
		if strings.Contains(s, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
