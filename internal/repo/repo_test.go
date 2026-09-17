package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimalyad/osspipeline/internal/identity"
	"github.com/vimalyad/osspipeline/internal/model"
)

// upstream builds a throwaway "remote" with a few commits, so clone, branch
// and takeover can be exercised against real git rather than a mock.
func upstream(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Up Stream", "GIT_AUTHOR_EMAIL=up@example.com",
			"GIT_COMMITTER_NAME=Up Stream", "GIT_COMMITTER_EMAIL=up@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, f := range []string{"README.md", "src/app.go"} {
		p := filepath.Join(d, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("hello\n"), 0o644)
	}
	run("add", "-A")
	run("commit", "-qm", "feat: initial commit")
	run("commit", "-q", "--allow-empty", "-m", "fix: another thing")
	return d
}

func manager(t *testing.T, root string) *Manager {
	t.Helper()
	cfg := filepath.Join(root, "config")
	os.MkdirAll(cfg, 0o755)
	os.WriteFile(filepath.Join(cfg, "identity.env"), []byte(`
OSS_LOGIN="someone"
OSS_NAME="Some One"
OSS_EMAIL="1+someone@users.noreply.github.com"
OSS_KEYCHAIN_ACCOUNT="someone"
OSS_KEYCHAIN_SERVICE="svc"
WORK_LOGIN="workacct"
WORK_EMAIL="2+workacct@users.noreply.github.com"
OTHER_LOGINS="workacct"
OTHER_EMAILS="2+workacct@users.noreply.github.com"
`), 0o644)
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "bin", "gh-token-helper"), []byte("#!/bin/sh\n"), 0o755)
	os.MkdirAll(filepath.Join(root, "githooks"), 0o755)
	id, err := identity.Load(filepath.Join(cfg, "identity.env"))
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{Root: root, ID: id, Env: os.Environ()}
}

func clone(t *testing.T, m *Manager, src string) string {
	t.Helper()
	dir := m.Dir("acme/widget")
	os.MkdirAll(filepath.Dir(dir), 0o755)
	if out, err := exec.Command("git", "clone", "-q", src, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if err := identity.HardenClone(dir, m.ID); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A clone is where the identity guarantees have to survive a repository that
// can rewrite git config, so hardening must hold immediately after cloning.
func TestPreparedCloneAssertsIdentity(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	if err := identity.AssertClone(dir, m.ID); err != nil {
		t.Fatalf("a prepared clone must pass its own guard: %v", err)
	}
}

func TestDefaultBranchIsReadNotAssumed(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	if got := m.DefaultBranch(context.Background(), dir); got != "main" {
		t.Fatalf("default branch = %q", got)
	}
}

func TestSanityRejectsAWreckedTree(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	ctx := context.Background()

	if ok, why := m.Sanity(ctx, dir); !ok {
		t.Fatalf("a fresh clone is sane: %s", why)
	}
	// Rewrite every tracked file, as a clone interrupted mid-checkout does.
	for _, f := range []string{"README.md", "src/app.go"} {
		os.WriteFile(filepath.Join(dir, f), []byte("corrupted"), 0o644)
	}
	ok, why := m.Sanity(ctx, dir)
	if ok {
		t.Fatal("a tree where everything differs is a broken clone")
	}
	if !strings.Contains(why, "clone is broken") {
		t.Errorf("why = %q", why)
	}
}

func TestBranchStartsFromUpstream(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	br, err := m.Branch(context.Background(), dir, &model.Candidate{Repo: "acme/widget", Issue: 42})
	if err != nil {
		t.Fatal(err)
	}
	if br != "fix/issue-42" {
		t.Fatalf("branch = %q", br)
	}
}

// Rebuilding work last touched years ago produces a diff nobody can review.
func TestTakeoverRefusesAncientWork(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	old := 3 * 365
	c := &model.Candidate{Repo: "acme/widget", Issue: 7,
		PRSignal: &model.PRSignal{Number: 99, Author: "someone", DaysSinceCommit: &old}}
	_, why, err := m.Takeover(context.Background(), dir, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(why, "too old to rebase") {
		t.Fatalf("why = %q", why)
	}
}

func TestTakeoverWithNoPriorPRStartsFresh(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	_, why, err := m.Takeover(context.Background(), dir,
		&model.Candidate{Repo: "acme/widget", Issue: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(why, "no prior PR") {
		t.Fatalf("why = %q", why)
	}
}

// The guard needs the net shipped set, which comes from these two listings.
func TestNameStatusReportsCommittedAndPending(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	ctx := context.Background()
	m.Branch(ctx, dir, &model.Candidate{Repo: "acme/widget", Issue: 1})

	os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("x"), 0o644)
	exec.Command("git", "-C", dir, "add", "-A").Run()
	cmd := exec.Command("git", "-C", dir, "commit", "-qm", "add a file")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Some One",
		"GIT_AUTHOR_EMAIL=1+someone@users.noreply.github.com",
		"GIT_COMMITTER_NAME=Some One",
		"GIT_COMMITTER_EMAIL=1+someone@users.noreply.github.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "pending.txt"), []byte("y"), 0o644)

	committed, pending := m.NameStatus(ctx, dir)
	if !strings.Contains(committed, "committed.txt") {
		t.Errorf("committed listing = %q", committed)
	}
	if !strings.Contains(pending, "pending.txt") {
		t.Errorf("pending listing = %q", pending)
	}
}

func TestCommitStyleReadsRealHistory(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	style := m.CommitStyle(context.Background(), dir)
	if !strings.Contains(style, "feat:") && !strings.Contains(style, "fix:") {
		t.Fatalf("style = %q; should sample real subjects", style)
	}
}

func TestTopLevelEntries(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	got := m.TopLevelEntries(context.Background(), dir)
	if !got["src"] || !got["README.md"] {
		t.Fatalf("entries = %v", got)
	}
}

// TestUntrackedAgentFileIsVisibleToTheGuard is the regression test for a hole
// found while porting: `git diff --name-status HEAD` reports tracked changes
// only, so a file the implementer created and never committed was invisible to
// preflight -- and commit runs `git add -A`, which would have shipped it.
func TestUntrackedAgentFileIsVisibleToTheGuard(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	ctx := context.Background()
	m.Branch(ctx, dir, &model.Candidate{Repo: "acme/widget", Issue: 1})

	os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents", "skills", "SKILL.md"),
		[]byte("agent workflow notes\n"), 0o644)

	_, pending := m.NameStatus(ctx, dir)
	if !strings.Contains(pending, ".agents/skills/SKILL.md") {
		t.Fatalf("an untracked file that `git add -A` would commit must be "+
			"visible to the guard; pending was %q", pending)
	}
}

// Ignored files must NOT appear, or every clone with a .venv or node_modules
// reports hundreds of phantom additions.
func TestIgnoredFilesStayOutOfTheShippedSet(t *testing.T) {
	m := manager(t, t.TempDir())
	dir := clone(t, m, upstream(t))
	ctx := context.Background()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "build"), 0o755)
	os.WriteFile(filepath.Join(dir, "build", "out.bin"), []byte("x"), 0o644)

	_, pending := m.NameStatus(ctx, dir)
	if strings.Contains(pending, "build/out.bin") {
		t.Fatalf("ignored files must not be reported: %q", pending)
	}
}
