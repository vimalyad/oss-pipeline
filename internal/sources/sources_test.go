package sources

import (
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/profile"
)

var now = time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

func domains() []profile.Domain {
	return []profile.Domain{
		{ID: "python-cv", Weight: 3, Languages: []string{"Python"},
			Topics:    []string{"computer-vision", "pytorch"},
			SeedRepos: []string{"kornia/kornia", "pytorch/vision", "huggingface/datasets"}},
		{ID: "devops-go", Weight: 1, Languages: []string{"Go"},
			Topics:    []string{"kubernetes", "cli"},
			SeedRepos: []string{"helm/helm", "kubernetes-sigs/kind"}},
	}
}

// TestWeightDrivesTheShare is the whole point of a weighted sweep: raising one
// number in the profile is how the pipeline is steered, with no code change.
func TestWeightDrivesTheShare(t *testing.T) {
	slots, _ := Allocate(domains(), 8, Cursor{})
	counts := map[string]int{}
	for _, s := range slots {
		counts[s.Domain]++
	}
	if len(slots) != 8 {
		t.Fatalf("got %d slots, want 8", len(slots))
	}
	// 3:1 over 8 slots is 6 and 2.
	if counts["python-cv"] != 6 || counts["devops-go"] != 2 {
		t.Fatalf("shares = %v, want python-cv 6 and devops-go 2", counts)
	}
}

// TestEveryDomainIsTouchedEarly: a pass cut short by a rate limit should still
// have looked at every domain, not run the first one to exhaustion.
func TestEveryDomainIsTouchedEarly(t *testing.T) {
	slots, _ := Allocate(domains(), 8, Cursor{})
	seen := map[string]bool{}
	for _, s := range slots[:4] {
		seen[s.Domain] = true
	}
	if len(seen) != 2 {
		t.Fatalf("after 4 slots only %v had been looked at", seen)
	}
}

// TestTheSweepResumes is the v1 defect this exists to prevent: the watchlist
// was walked from the top every run and the budget ran out in the same place,
// so the last 18 of 25 repositories were never examined at all.
func TestTheSweepResumes(t *testing.T) {
	ds := domains()
	first, cur := Allocate(ds, 2, Cursor{})
	second, cur2 := Allocate(ds, 2, cur)

	if first[0].Repo == second[0].Repo {
		t.Fatalf("the second pass restarted at %q", second[0].Repo)
	}
	// Across enough passes every seed repository must be reached.
	reached := map[string]bool{}
	for _, s := range append(first, second...) {
		reached[s.Repo] = true
	}
	c := cur2
	for i := 0; i < 10; i++ {
		var got []Slot
		got, c = Allocate(ds, 2, c)
		for _, s := range got {
			reached[s.Repo] = true
		}
	}
	for _, d := range ds {
		for _, r := range d.SeedRepos {
			if !reached[r] {
				t.Errorf("%s was never swept", r)
			}
		}
	}
}

func TestAllocateEdgeCases(t *testing.T) {
	if s, _ := Allocate(nil, 5, Cursor{}); s != nil {
		t.Error("allocated slots with no domains")
	}
	if s, _ := Allocate(domains(), 0, Cursor{}); s != nil {
		t.Error("allocated slots with no budget")
	}
	// A domain with no seed repos must not wedge the loop.
	ds := append(domains(), profile.Domain{ID: "empty", Weight: 5})
	got, _ := Allocate(ds, 4, Cursor{})
	if len(got) != 4 {
		t.Fatalf("got %d slots, want 4", len(got))
	}
	for _, s := range got {
		if s.Domain == "empty" {
			t.Error("allocated a slot to a domain with no repositories")
		}
	}
	// A weight of zero is treated as one rather than starving the domain.
	zero := []profile.Domain{{ID: "z", Weight: 0, SeedRepos: []string{"a/b"}}}
	if got, _ := Allocate(zero, 2, Cursor{}); len(got) != 2 {
		t.Errorf("a zero-weight domain got %d slots", len(got))
	}
}

// TestBudgetLargerThanTotalWeightIsStillUsed: otherwise a generous budget
// silently sweeps fewer repositories than it was given.
func TestBudgetLargerThanTotalWeightIsStillUsed(t *testing.T) {
	got, _ := Allocate(domains(), 20, Cursor{})
	if len(got) != 20 {
		t.Fatalf("got %d slots for a budget of 20", len(got))
	}
}

func TestRepoQueryUsesTopics(t *testing.T) {
	var disc profile.Discovery
	disc.QuarantineDays = 14
	disc.OpenSearch.MinStars = 800
	disc.OpenSearch.MaxStars = 120000
	disc.OpenSearch.PushedWithinDays = 60

	q := RepoQuery(domains()[0], disc, now)
	for _, want := range []string{
		"topic:computer-vision", "topic:pytorch", "language:python",
		"stars:800..120000", "pushed:>=2026-07-24", "archived:false", "template:false",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q is missing %q", q, want)
		}
	}
}

func TestRepoQueryWithOnlyAMinimum(t *testing.T) {
	var disc profile.Discovery
	disc.OpenSearch.MinStars = 800
	if q := RepoQuery(domains()[0], disc, now); !strings.Contains(q, "stars:>=800") {
		t.Errorf("q = %q", q)
	}
}

// TestIssueQueryNeverCarriesATopic is the one that matters. GitHub's issue
// search has no `topic:` qualifier and does not reject one -- it treats it as
// free text and returns plausible results -- so this mistake is invisible
// without a test that looks for it.
func TestIssueQueryNeverCarriesATopic(t *testing.T) {
	q := IssueQuery("kornia/kornia", []string{"good first issue", "help wanted"}, now, 365)
	if strings.Contains(q, "topic:") {
		t.Fatalf("issue query carries a topic qualifier: %q", q)
	}
	for _, want := range []string{
		"repo:kornia/kornia", "is:issue", "is:open", "no:assignee",
		`label:"good first issue"`, `label:"help wanted"`, "updated:>=2025-09-22",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q is missing %q", q, want)
		}
	}
}

func TestQuarantine(t *testing.T) {
	tests := []struct {
		name string
		a    Admission
		want bool
	}{
		{"a repo the user named is never quarantined",
			Admission{Origin: OriginSeed, FirstSeen: now}, false},
		{"a freshly discovered repo is",
			Admission{Origin: OriginSearch, FirstSeen: now}, true},
		{"one found 13 days ago still is",
			Admission{Origin: OriginSearch, FirstSeen: now.AddDate(0, 0, -13)}, true},
		{"one found 14 days ago is not",
			Admission{Origin: OriginSearch, FirstSeen: now.AddDate(0, 0, -14)}, false},
		// A record written before this field existed must not be promoted by
		// its own emptiness.
		{"an unknown first-seen is treated as new",
			Admission{Origin: OriginSearch}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Quarantined(now, 14); got != tt.want {
				t.Errorf("= %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSeedAdmissionsAreDeduplicatedAndOrdered(t *testing.T) {
	p := &profile.Profile{Domains: []profile.Domain{
		{ID: "b", SeedRepos: []string{"z/z", "a/a"}},
		{ID: "a", SeedRepos: []string{"a/a"}},
	}}
	got := SeedAdmissions(p, now)
	if len(got) != 2 {
		t.Fatalf("got %d admissions, want 2 after dedupe: %+v", len(got), got)
	}
	if got[0].Repo != "a/a" || got[1].Repo != "z/z" {
		t.Errorf("not ordered: %+v", got)
	}
	for _, a := range got {
		if a.Quarantined(now, 14) {
			t.Errorf("%s was quarantined despite being a seed", a.Repo)
		}
	}
}
