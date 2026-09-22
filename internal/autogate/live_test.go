package autogate

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/profile"
	"github.com/vimalyad/osspipeline/internal/store"
)

// TestLiveShadowModeOpensNothing is the parity gate for this group.
//
// It runs the real decision against every candidate on disk, with the real
// profile and no grants, and asserts that nothing at all is authorised. The
// assertion is not that the numbers look sensible: it is that the count is
// zero, because the autonomous path has never been switched on and a single
// allowed candidate here would mean it had been, silently.
//
//	OSSP_LIVE=1 go test ./internal/autogate -run TestLiveShadow -v
func TestLiveShadowModeOpensNothing(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1 to decide against the real state")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	p, err := profile.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(root)
	all, bad := s.All()
	if len(bad) > 0 {
		t.Fatalf("%d candidates unreadable", len(bad))
	}

	at := time.Now()
	var allowed, wouldAllow int
	reasons := map[string]int{}

	for _, c := range all {
		var lang string
		if c.Facts != nil {
			lang = c.Facts.PrimaryLanguage
		}
		d, ok := p.ForRepo(c.Repo, lang, nil)
		if !ok {
			continue
		}
		// No grants exist, which is itself the point: the zero value of a
		// Grant must authorise nothing.
		in := Input{
			Now: at, Want: profile.AutonomyOpenPRs, Candidate: c, Domain: d,
			Probation: Probation{MinDecided: 6, MinRate: 0.5},
			Caps:      Caps{PerDay: 1, MaxOpen: 2},
		}
		dec := Decide(in)
		if dec.Allowed {
			allowed++
			t.Errorf("AUTHORISED: %s — %s", c.Slug(), dec.Why())
		}
		if dec.WouldAllow {
			wouldAllow++
		}
		for _, b := range dec.Blockers {
			reasons[firstClause(b)]++
		}
	}

	fmt.Printf("candidates considered: %d\n", len(all))
	fmt.Printf("allowed: %d   would allow but for the trial: %d\n", allowed, wouldAllow)
	fmt.Println("refusal reasons:")
	for _, r := range sortedByCount(reasons) {
		fmt.Printf("  %4d  %s\n", reasons[r], r)
	}

	if allowed != 0 {
		t.Fatalf("shadow mode authorised %d candidate(s); it must authorise none", allowed)
	}
	// A run where nothing was even considered would pass vacuously.
	if len(reasons) == 0 {
		t.Fatal("no candidate reached a decision; this proves nothing")
	}
}

// TestLiveProposalsAreAllRefused checks the eight awaiting approval
// specifically, since those are the ones an autonomous path would act on first.
func TestLiveProposalsAreAllRefused(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	p, err := profile.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(root)
	proposed := s.ByStatus(model.StatusProposed)
	if len(proposed) == 0 {
		t.Skip("nothing proposed")
	}
	for _, c := range proposed {
		var lang string
		if c.Facts != nil {
			lang = c.Facts.PrimaryLanguage
		}
		d, _ := p.ForRepo(c.Repo, lang, nil)
		dec := Decide(Input{
			Now: time.Now(), Want: profile.AutonomyOpenPRs, Candidate: c, Domain: d,
			Probation: Probation{MinDecided: 6, MinRate: 0.5},
			Caps:      Caps{PerDay: 1, MaxOpen: 2},
		})
		fmt.Printf("%-34s allowed=%v  %s\n", c.Slug(), dec.Allowed, dec.Why())
		if dec.Allowed {
			t.Errorf("%s would have been opened without anyone approving it", c.Slug())
		}
	}
}

func firstClause(s string) string {
	for i, r := range s {
		if r == ':' || r == '(' {
			return s[:i]
		}
	}
	if len(s) > 60 {
		return s[:60]
	}
	return s
}

func sortedByCount(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && m[keys[j]] > m[keys[j-1]]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
