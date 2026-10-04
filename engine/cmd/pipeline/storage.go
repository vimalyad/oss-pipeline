package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/vimalyad/oss-pipeline/engine/internal/audit"
	"github.com/vimalyad/oss-pipeline/engine/internal/model"
	"github.com/vimalyad/oss-pipeline/engine/internal/store"
	"github.com/vimalyad/oss-pipeline/engine/internal/store/pg"
	"github.com/vimalyad/oss-pipeline/engine/internal/watch"
)

// candidateStore is everything the commands ask of persistence. Both stores
// satisfy it, asserted below, and nothing in this package names either one
// outside this file -- which is what lets DATABASE_URL alone decide where a
// run's state lives.
type candidateStore interface {
	Load(slug string) (*model.Candidate, error)
	Save(c *model.Candidate) (string, error)
	All() ([]*model.Candidate, []store.LoadResult)
	ByStatus(want ...model.Status) []*model.Candidate
	ShouldReconsider(slug string, now time.Time, afterDays int) bool
	LoadRepoFacts(repo string) (*model.RepoFacts, error)
	SaveRepoFacts(f *model.RepoFacts) error
}

var (
	_ candidateStore = (*store.Store)(nil)
	_ candidateStore = (*pg.Store)(nil)
)

// One store per process. The commands are one-shots and several of them --
// daily above all -- open the store from more than one stage; a pool per call
// would hold a connection per stage for the life of the process.
var (
	storeOnce sync.Once
	theStore  candidateStore
	storeErr  error
)

// openStore picks the backend. DATABASE_URL set means Postgres and nothing
// else: a configured database that cannot be reached is an error, never a
// quiet fall back to files, because a run that wrote its decisions to the
// other store would leave the dashboard and the engine disagreeing about what
// was approved.
func openStore(root string) (candidateStore, error) {
	storeOnce.Do(func() {
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			theStore = store.New(root)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s, err := pg.Open(ctx, url)
		if err != nil {
			storeErr = fmt.Errorf("DATABASE_URL is set but the database is unusable: %w", err)
			return
		}
		theStore = s
	})
	return theStore, storeErr
}

// mustStore is openStore for the commands, which all have the same thing to
// say about a missing database and nothing useful to do without one.
func mustStore(root string) (candidateStore, bool) {
	st, err := openStore(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, false
	}
	return st, true
}

// backendName says which store a run is using, for doctor.
func backendName(st candidateStore) string {
	switch st.(type) {
	case *pg.Store:
		return "postgres (DATABASE_URL)"
	default:
		return "json files (state/)"
	}
}

// auditor is what every command records with.
type auditor interface {
	Record(action, slug, detail string) error
}

// teeAudit writes the file log and, when the database is the store, the audit
// table too.
//
// Both, not one: the file is what recovered sixteen destroyed decisions in the
// predecessor and costs nothing to keep, and the table is what the dashboard
// reads. A failure of either is returned, but the other has still been
// written.
type teeAudit struct {
	file *audit.Log
	db   *pg.Store
}

func (t teeAudit) Record(action, slug, detail string) error {
	ferr := t.file.Record(action, slug, detail)
	if t.db == nil {
		return ferr
	}
	if derr := t.db.Record(action, slug, detail); derr != nil {
		return derr
	}
	return ferr
}

func openAudit(root string) auditor {
	t := teeAudit{file: audit.New(root)}
	if st, err := openStore(root); err == nil {
		if db, ok := st.(*pg.Store); ok {
			t.db = db
		}
	}
	return t
}

// prObserver hands a watch cycle's reading to the database's pull request
// view, or is nil when the store has none: the file store keeps nothing per
// pull request beyond the candidate.
func prObserver(st candidateStore) func(*model.Candidate, watch.State) error {
	db, ok := st.(*pg.Store)
	if !ok {
		return nil
	}
	return func(c *model.Candidate, s watch.State) error {
		return db.ObservePR(c.Slug(), observation(c, s))
	}
}

// observation translates the watcher's terms into the schema's.
func observation(c *model.Candidate, s watch.State) pg.PRObservation {
	url := s.URL
	if url == "" {
		url = c.PRURL
	}
	number := s.Number
	if number == 0 && c.PRNumber != nil {
		number = *c.PRNumber
	}
	// GitHub reports zero for a diff it has not computed, which is
	// indistinguishable from an empty diff; an empty pull request is not
	// something this pipeline opens, so zero is read as unknown.
	opt := func(n int) *int {
		if n == 0 {
			return nil
		}
		return &n
	}
	state := watch.PRState(s, c.Status)
	return pg.PRObservation{
		Number: number, URL: url, Title: s.Title,
		HeadBranch: s.HeadBranch, HeadSHA: s.HeadSHA, BaseBranch: s.BaseBranch,
		State:       state,
		ChecksTotal: s.ChecksTotal, ChecksFailing: len(s.Failing), ChecksPending: s.ChecksPending,
		ReviewDecision: s.ReviewDecision, ReviewerCount: s.Reviewers,
		Additions: opt(s.Additions), Deletions: opt(s.Deletions), ChangedFiles: opt(s.ChangedFiles),
		RemoteUpdatedAt: s.UpdatedAt, MergedAt: s.MergedAt, ClosedAt: s.ClosedAt,
		Detail: fmt.Sprintf("watch: %s, candidate %s", state, c.Status),
	}
}
