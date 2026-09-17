// Package profile is the user's declared interests: which domains they want to
// contribute in, how much weight each gets, and how much autonomy each has.
//
// v1 had four domain keys in the watchlist and threw them away at load, so the
// label never reached a candidate, a score or a report. Here a domain is a
// weighted (topics x languages) pair, because topics are the axis GitHub
// actually indexes -- and the repo metadata call already fetches them.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Autonomy levels, strictly ordered. `fix` sits below `reply` deliberately:
// pushing a lint fix is recoverable, posting under the user's name is not.
type Autonomy string

const (
	AutonomyOff     Autonomy = "off"
	AutonomyShadow  Autonomy = "shadow"
	AutonomyFix     Autonomy = "fix"
	AutonomyReply   Autonomy = "reply"
	AutonomyOpenPRs Autonomy = "open_prs"
)

var autonomyRank = map[Autonomy]int{
	AutonomyOff: 0, AutonomyShadow: 1, AutonomyFix: 2,
	AutonomyReply: 3, AutonomyOpenPRs: 4,
}

// AtLeast reports whether this level permits an action needing `want`.
func (a Autonomy) AtLeast(want Autonomy) bool {
	return autonomyRank[a] >= autonomyRank[want]
}

func (a Autonomy) Valid() bool { _, ok := autonomyRank[a]; return ok }

// Domain is one area the user wants to contribute in.
type Domain struct {
	ID        string   `yaml:"id"`
	Label     string   `yaml:"label"`
	Weight    int      `yaml:"weight"`
	Languages []string `yaml:"languages"`
	Topics    []string `yaml:"topics"`
	Keywords  []string `yaml:"keywords"`
	SeedRepos []string `yaml:"seed_repos"`
	Autonomy  Autonomy `yaml:"autonomy"`
}

type Discovery struct {
	Mode               string `yaml:"mode"`
	RequireDomainMatch bool   `yaml:"require_domain_match"`
	QuarantineDays     int    `yaml:"quarantine_days"`
	OpenSearch         struct {
		Enabled           bool `yaml:"enabled"`
		MinStars          int  `yaml:"min_stars"`
		MaxStars          int  `yaml:"max_stars"`
		PushedWithinDays  int  `yaml:"pushed_within_days"`
		MinGoodFirstIssue int  `yaml:"min_good_first_issues"`
		NewReposPerDomain int  `yaml:"new_repos_per_domain"`
	} `yaml:"open_search"`
}

type Notifications struct {
	Channel string `yaml:"channel"`
	Ntfy    struct {
		Server           string `yaml:"server"`
		TopicFile        string `yaml:"topic_file"`
		CommandTopicFile string `yaml:"command_topic_file"`
	} `yaml:"ntfy"`
	QuietHours struct {
		Start string `yaml:"start"`
		End   string `yaml:"end"`
		TZ    string `yaml:"tz"`
	} `yaml:"quiet_hours"`
	DigestAt string `yaml:"digest_at"`
}

// Profile is the whole declared configuration for one user.
type Profile struct {
	Schema        int           `yaml:"schema"`
	User          string        `yaml:"user"`
	DisplayName   string        `yaml:"display_name"`
	Domains       []Domain      `yaml:"domains"`
	Discovery     Discovery     `yaml:"discovery"`
	Notifications Notifications `yaml:"notifications"`
}

// Paths is the multi-tenancy seam. Today every user resolves to the same
// directories; making it a function now means the later change is one body,
// not a sweep through every package that touches state.
type Paths struct {
	User       string
	Root       string
	State      string
	Candidates string
	Repos      string
	Reports    string
	Work       string
}

func (p *Profile) Paths(root string) Paths {
	state := filepath.Join(root, "state")
	return Paths{
		User: p.User, Root: root, State: state,
		Candidates: filepath.Join(state, "candidates"),
		Repos:      filepath.Join(state, "repos"),
		Reports:    filepath.Join(root, "reports"),
		Work:       filepath.Join(root, "work"),
	}
}

// Domain returns a domain by id.
func (p *Profile) Domain(id string) (Domain, bool) {
	for _, d := range p.Domains {
		if d.ID == id {
			return d, true
		}
	}
	return Domain{}, false
}

// SeedRepos is every declared repo paired with the domain that declared it.
func (p *Profile) SeedRepos() []RepoDomain {
	var out []RepoDomain
	for _, d := range p.Domains {
		for _, r := range d.SeedRepos {
			out = append(out, RepoDomain{Repo: r, Domain: d.ID, Weight: d.Weight})
		}
	}
	return out
}

type RepoDomain struct {
	Repo   string
	Domain string
	Weight int
}

// ForRepo picks the domain a repo belongs to, scoring each and taking the
// best. Returns ok=false when nothing matches, which the scorer turns into
// either a rejection or a ranking penalty depending on configuration.
func (p *Profile) ForRepo(repo, language string, topics []string) (Domain, bool) {
	type scored struct {
		d Domain
		n int
	}
	var best []scored
	for _, d := range p.Domains {
		n := 0
		for _, seed := range d.SeedRepos {
			if strings.EqualFold(seed, repo) {
				n += 3
				break
			}
		}
		for _, l := range d.Languages {
			if strings.EqualFold(l, language) {
				n += 2
				break
			}
		}
		for _, t := range d.Topics {
			for _, got := range topics {
				if strings.EqualFold(t, got) {
					n++
				}
			}
		}
		low := strings.ToLower(repo)
		for _, k := range d.Keywords {
			if strings.Contains(low, strings.ToLower(k)) {
				n++
				break
			}
		}
		if n > 0 {
			best = append(best, scored{d, n})
		}
	}
	if len(best) == 0 {
		return Domain{}, false
	}
	// Deterministic: score, then weight, then id. A sweep that reorders
	// itself between runs is untestable.
	sort.Slice(best, func(i, j int) bool {
		if best[i].n != best[j].n {
			return best[i].n > best[j].n
		}
		if best[i].d.Weight != best[j].d.Weight {
			return best[i].d.Weight > best[j].d.Weight
		}
		return best[i].d.ID < best[j].d.ID
	})
	return best[0].d, true
}

var (
	cacheMu   sync.Mutex
	cached    *Profile
	cachedMod time.Time
)

// Load reads config/profile.yaml, caching on the file's modification time so
// edits take effect without a restart -- the property v1 relied on.
func Load(root string) (*Profile, error) {
	path := filepath.Join(root, "config", "profile.yaml")
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cached != nil && st.ModTime().Equal(cachedMod) {
		return cached, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	cached, cachedMod = &p, st.ModTime()
	return &p, nil
}

func (p *Profile) validate() error {
	if p.User == "" {
		return fmt.Errorf("profile: user is required")
	}
	if len(p.Domains) == 0 {
		return fmt.Errorf("profile: at least one domain is required")
	}
	seen := map[string]bool{}
	for i := range p.Domains {
		d := &p.Domains[i]
		if d.ID == "" {
			return fmt.Errorf("profile: domain %d has no id", i)
		}
		if seen[d.ID] {
			return fmt.Errorf("profile: duplicate domain id %q", d.ID)
		}
		seen[d.ID] = true
		if d.Weight <= 0 {
			d.Weight = 1
		}
		if d.Autonomy == "" {
			d.Autonomy = AutonomyOff
		}
		if !d.Autonomy.Valid() {
			return fmt.Errorf("profile: domain %q has unknown autonomy %q", d.ID, d.Autonomy)
		}
	}
	return nil
}
