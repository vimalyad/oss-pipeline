package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vimalyad/oss-pipeline/engine/internal/model"
	"github.com/vimalyad/oss-pipeline/engine/internal/store"
	"github.com/vimalyad/oss-pipeline/engine/internal/store/pg"
)

// importJSONCmd copies the file store into Postgres.
//
// Every history entry goes in with its original timestamp and its forced and
// actor flags, through the same Save the engine uses, so the transition
// trigger judges the imported history exactly as it would judge a live run.
// An edge the table refuses is reported and that candidate skipped -- the
// predecessor's files carried hand-written edges, and quietly importing them
// would put rule-breaking history behind the rule.
//
// Safe to rerun. Save appends only the history the database lacks, so a
// second import of unchanged files writes no new history; and a candidate the
// database has moved past since the first import -- approved on the dashboard,
// say -- is refused as stale rather than rolled back to what the file says.
func importJSONCmd(root string, args []string) int {
	execute := false
	for _, a := range args {
		if a == "--execute" {
			execute = true
		} else {
			fmt.Fprintf(os.Stderr, "unknown argument %q\n", a)
			return 2
		}
	}
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "error: DATABASE_URL is not set; there is nowhere to import to")
		return 1
	}
	st, ok := mustStore(root)
	if !ok {
		return 1
	}
	db, isPG := st.(*pg.Store)
	if !isPG {
		fmt.Fprintln(os.Stderr, "error: the active store is not Postgres")
		return 1
	}

	files := store.New(root)
	cands, bad := files.All()
	for _, b := range bad {
		fmt.Fprintf(os.Stderr, "unreadable, not imported: %s: %v\n", b.Slug, b.Err)
	}
	facts := repoFactFiles(root, files)

	if !execute {
		fmt.Printf("[dry run] would import %d candidate(s) and %d repository fact record(s)\n",
			len(cands), len(facts))
		fmt.Println("run with --execute to write them")
		if len(bad) > 0 {
			return 1
		}
		return 0
	}

	// Facts first: a candidate's repo row is created bare by Save, and the
	// facts filling it in afterwards would work, but this order means a
	// failed candidate import still leaves its repository's facts in place.
	factFails := 0
	for _, f := range facts {
		if err := db.SaveRepoFacts(f); err != nil {
			fmt.Fprintf(os.Stderr, "facts %s: %v\n", f.Repo, err)
			factFails++
		}
	}

	var saved, stale, refused, failed int
	for _, c := range cands {
		_, err := db.Save(c)
		switch {
		case err == nil:
			saved++
		case errors.Is(err, pg.ErrStale):
			stale++
			fmt.Printf("  newer in the database, left alone: %v\n", err)
		case errors.Is(err, model.ErrIllegalTransition):
			refused++
			fmt.Printf("  REFUSED %s: %v\n", c.Slug(), err)
		default:
			failed++
			fmt.Fprintf(os.Stderr, "  FAILED %s: %v\n", c.Slug(), err)
		}
	}
	_ = db.Record("import_json", "", fmt.Sprintf(
		"saved=%d stale=%d refused=%d failed=%d facts=%d", saved, stale, refused, failed, len(facts)-factFails))

	fmt.Printf("\ncandidates: %d saved, %d newer in the database, %d refused by the state machine, %d failed\n",
		saved, stale, refused, failed)
	fmt.Printf("repository facts: %d saved, %d failed\n", len(facts)-factFails, factFails)
	if refused > 0 {
		fmt.Println("\nA refused candidate has an edge in its file the transition table does not allow;\n" +
			"`pipeline doctor` without DATABASE_URL lists them. Correct the file and rerun.")
	}
	if len(bad)+refused+failed+factFails > 0 {
		return 1
	}
	return 0
}

// repoFactFiles reads every cached repository record the file store holds.
// The file store has no listing method, because nothing before the import
// needed one; the names are its own encoding of owner/name.
func repoFactFiles(root string, files *store.Store) []*model.RepoFacts {
	paths, _ := filepath.Glob(filepath.Join(root, "state", "repos", "*.json"))
	sort.Strings(paths)
	var out []*model.RepoFacts
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		repo := strings.Replace(name, "__", "/", 1)
		f, err := files.LoadRepoFacts(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "unreadable facts, not imported: %s: %v\n", name, err)
			continue
		}
		if f.Repo == "" {
			f.Repo = repo
		}
		out = append(out, f)
	}
	return out
}
