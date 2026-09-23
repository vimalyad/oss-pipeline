// Package store persists candidates as one JSON file each.
//
// Not a database, deliberately: when an unattended run wedges, being able to
// read the state with `cat` and correct it in a text editor has repeatedly
// been what made the failure diagnosable.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vimalyad/osspipeline/internal/model"
)

// Store reads and writes candidate files under a root directory.
type Store struct {
	root string // the pipeline root, containing state/
}

func New(root string) *Store { return &Store{root: root} }

func (s *Store) dir() string      { return filepath.Join(s.root, "state", "candidates") }
func (s *Store) reposDir() string { return filepath.Join(s.root, "state", "repos") }

// PathFor is the file a slug lives at.
func (s *Store) PathFor(slug string) string {
	return filepath.Join(s.dir(), slug+".json")
}

// Load reads one candidate.
func (s *Store) Load(slug string) (*model.Candidate, error) {
	b, err := os.ReadFile(s.PathFor(slug))
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", slug, err)
	}
	return decode(b, slug)
}

func decode(b []byte, slug string) (*model.Candidate, error) {
	var c model.Candidate
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", slug, err)
	}
	// A status outside the table means the file was hand-edited wrongly.
	// Say so rather than carrying on with something the state machine cannot
	// reason about -- v1 tolerated this and accumulated three bad files.
	if !c.Status.Valid() {
		return nil, fmt.Errorf("parse %s: unknown status %q", slug, c.Status)
	}
	return &c, nil
}

// Save writes a candidate atomically: a crash mid-write must not truncate a
// file the next run has to read.
func (s *Store) Save(c *model.Candidate) (string, error) {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	final := s.PathFor(c.Slug())
	tmp, err := os.CreateTemp(s.dir(), ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	return final, nil
}

// LoadResult carries one file's outcome so a single corrupt file does not
// hide the other 223.
type LoadResult struct {
	Slug string
	Cand *model.Candidate
	Err  error
}

// All reads every candidate. Unreadable files are reported, not skipped
// silently, and never abort the sweep.
func (s *Store) All() ([]*model.Candidate, []LoadResult) {
	paths, _ := filepath.Glob(filepath.Join(s.dir(), "*.json"))
	sort.Strings(paths)
	var ok []*model.Candidate
	var bad []LoadResult
	for _, p := range paths {
		slug := strings.TrimSuffix(filepath.Base(p), ".json")
		b, err := os.ReadFile(p)
		if err != nil {
			bad = append(bad, LoadResult{Slug: slug, Err: err})
			continue
		}
		c, err := decode(b, slug)
		if err != nil {
			bad = append(bad, LoadResult{Slug: slug, Err: err})
			continue
		}
		ok = append(ok, c)
	}
	return ok, bad
}

// ByStatus returns candidates in any of the given statuses.
func (s *Store) ByStatus(want ...model.Status) []*model.Candidate {
	set := make(map[model.Status]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	all, _ := s.All()
	var out []*model.Candidate
	for _, c := range all {
		if set[c.Status] {
			out = append(out, c)
		}
	}
	return out
}

// LoadRepoFacts reads the cached per-repo gates, if present.
func (s *Store) LoadRepoFacts(repo string) (*model.RepoFacts, error) {
	name := strings.ReplaceAll(repo, "/", "__") + ".json"
	b, err := os.ReadFile(filepath.Join(s.reposDir(), name))
	if err != nil {
		return nil, err
	}
	var f model.RepoFacts
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse facts %s: %w", repo, err)
	}
	return &f, nil
}

// SaveRepoFacts writes the weekly repository cache.
//
// Written whole and atomically, like candidates: a torn facts file is loaded
// on the next run as an unparseable cache, which silently refetches every
// repository and burns the API budget a weekly cache exists to protect.
func (s *Store) SaveRepoFacts(f *model.RepoFacts) error {
	if f == nil || f.Repo == "" {
		return fmt.Errorf("store: facts with no repo")
	}
	if err := os.MkdirAll(s.reposDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	name := strings.ReplaceAll(f.Repo, "/", "__") + ".json"
	final := filepath.Join(s.reposDir(), name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}
