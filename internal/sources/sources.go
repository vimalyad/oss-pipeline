// Package sources decides which repositories a discovery pass looks at, and in
// what order.
//
// Two facts shape it.
//
// GitHub's issue search has no `topic:` qualifier. Topics exist only on
// repositories, so open-ended discovery has to admit repositories first and
// harvest their issues afterwards. Writing `topic:` into an issue query does
// not fail -- GitHub treats it as a free-text term and returns plausible
// rubbish -- so the mistake is invisible without a test.
//
// And a sweep must resume where the last one stopped. v1 walked its watchlist
// from the top every run and hit the per-run budget at the same place each
// time, so the last 18 of 25 repositories were never examined at all. The
// cursor here is why that cannot recur.
package sources

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vimalyad/osspipeline/internal/profile"
)

// Slot is one repository to examine in this pass, with the domain that earned
// it. Carrying the domain forward is what lets weight drive proposal ranking
// and per-domain merge rates rather than only the sweep.
type Slot struct {
	Domain string
	Repo   string
}

// Cursor records how far each domain's list has been walked, so successive
// passes continue rather than restarting.
type Cursor map[string]int

// Allocate picks this pass's slots, giving a domain of weight 3 three times
// the share of a domain of weight 1.
//
// It interleaves rather than running each domain to exhaustion: a pass that is
// cut short by a rate limit or a crash should still have looked at every
// domain, not the first one only.
func Allocate(domains []profile.Domain, budget int, cur Cursor) ([]Slot, Cursor) {
	if budget <= 0 || len(domains) == 0 {
		return nil, cur
	}
	next := Cursor{}
	for k, v := range cur {
		next[k] = v
	}

	ordered := append([]profile.Domain{}, domains...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	// remaining is how many slots each domain is still owed this pass.
	remaining := map[string]int{}
	total := 0
	for _, d := range ordered {
		w := d.Weight
		if w < 1 {
			w = 1
		}
		remaining[d.ID] = w
		total += w
	}

	var out []Slot
	for len(out) < budget {
		progressed := false
		for _, d := range ordered {
			if len(out) >= budget {
				break
			}
			if remaining[d.ID] <= 0 || len(d.SeedRepos) == 0 {
				continue
			}
			i := next[d.ID] % len(d.SeedRepos)
			out = append(out, Slot{Domain: d.ID, Repo: d.SeedRepos[i]})
			next[d.ID] = (i + 1) % len(d.SeedRepos)
			remaining[d.ID]--
			progressed = true
		}
		if !progressed {
			// Every domain has spent its weight. Refill and go round again,
			// so a budget larger than the total weight is still used.
			if total == 0 {
				break
			}
			refilled := false
			for _, d := range ordered {
				w := d.Weight
				if w < 1 {
					w = 1
				}
				if len(d.SeedRepos) > 0 {
					remaining[d.ID] = w
					refilled = true
				}
			}
			if !refilled {
				break
			}
		}
	}
	return out, next
}

// RepoQuery builds the GitHub repository-search query for a domain.
//
// Repositories, never issues: `topic:` is a repository qualifier, and the
// whole reason domains are expressed as topics is that topics are the axis
// GitHub actually indexes.
func RepoQuery(d profile.Domain, o profile.Discovery, now time.Time) string {
	var parts []string
	for _, t := range dedupeLower(d.Topics) {
		parts = append(parts, "topic:"+t)
	}
	for _, l := range dedupeLower(d.Languages) {
		parts = append(parts, fmt.Sprintf("language:%s", quoteIfSpaced(l)))
	}
	switch {
	case o.OpenSearch.MinStars > 0 && o.OpenSearch.MaxStars > 0:
		parts = append(parts, fmt.Sprintf("stars:%d..%d", o.OpenSearch.MinStars, o.OpenSearch.MaxStars))
	case o.OpenSearch.MinStars > 0:
		parts = append(parts, fmt.Sprintf("stars:>=%d", o.OpenSearch.MinStars))
	}
	if n := o.OpenSearch.PushedWithinDays; n > 0 {
		parts = append(parts, "pushed:>="+now.UTC().AddDate(0, 0, -n).Format("2006-01-02"))
	}
	// Archived or template repositories accept no contributions at all.
	parts = append(parts, "archived:false", "is:public", "template:false")
	return strings.Join(parts, " ")
}

// IssueQuery builds the issue search for one repository.
//
// It takes a repository rather than a domain, and that is the whole point: no
// topic qualifier can appear here. TestIssueQueryNeverCarriesATopic holds it,
// because GitHub accepts `topic:` in an issue query as a free-text term and
// returns results that look fine.
func IssueQuery(repo string, labels []string, now time.Time, updatedWithinDays int) string {
	parts := []string{"repo:" + repo, "is:issue", "is:open", "no:assignee"}
	for _, l := range labels {
		parts = append(parts, fmt.Sprintf("label:%s", quoteIfSpaced(l)))
	}
	if updatedWithinDays > 0 {
		parts = append(parts, "updated:>="+now.UTC().AddDate(0, 0, -updatedWithinDays).Format("2006-01-02"))
	}
	return strings.Join(parts, " ")
}

// Origin says how a repository entered the watchlist.
type Origin string

const (
	// OriginSeed is a repository the user named in their profile.
	OriginSeed Origin = "seed"
	// OriginSearch is a repository open-ended discovery found.
	OriginSearch Origin = "search"
)

// Admission is a repository's entry record.
type Admission struct {
	Repo      string    `json:"repo"`
	Domain    string    `json:"domain"`
	Origin    Origin    `json:"origin"`
	FirstSeen time.Time `json:"first_seen"`
}

// Quarantined reports whether a repository is still inside its settling
// period.
//
// Quarantine blocks the autonomous path only. A repository the pipeline found
// by itself is a weaker signal than one the user named, and the first
// unattended pull request into a project nobody has looked at is exactly the
// one that should not happen. Manual work is unaffected: a human looking at
// the proposal is the review the quarantine exists to require.
func (a Admission) Quarantined(now time.Time, days int) bool {
	if a.Origin == OriginSeed {
		return false // the user named it; that is the review.
	}
	if days <= 0 {
		return false
	}
	if a.FirstSeen.IsZero() {
		// Unknown is treated as new. The alternative silently promotes every
		// record written before this field existed.
		return true
	}
	return now.Before(a.FirstSeen.AddDate(0, 0, days))
}

// SeedAdmissions is every repository named in the profile, with its domain.
func SeedAdmissions(p *profile.Profile, now time.Time) []Admission {
	var out []Admission
	seen := map[string]bool{}
	for _, rd := range p.SeedRepos() {
		if seen[rd.Repo] {
			continue
		}
		seen[rd.Repo] = true
		out = append(out, Admission{
			Repo: rd.Repo, Domain: rd.Domain, Origin: OriginSeed, FirstSeen: now,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out
}

func dedupeLower(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		l := strings.ToLower(strings.TrimSpace(s))
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func quoteIfSpaced(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
