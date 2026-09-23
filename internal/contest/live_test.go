package contest

import (
	"fmt"
	"os"
	"testing"

	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/policy"
	"github.com/vimalyad/osspipeline/internal/store"
)

// TestStoredSignalsReclassifyIdentically is the parity gate for this package,
// and it needs no network: every candidate the Python implementation contested
// already has the auditable signal it decided from written next to its verdict.
// Re-deciding from the same facts must produce the same answer.
//
//	OSSP_LIVE=1 go test ./internal/contest -run TestStoredSignals -v
func TestStoredSignalsReclassifyIdentically(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1 to reclassify the real state")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	cfg, err := policy.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	all, bad := store.New(root).All()
	if len(bad) > 0 {
		t.Fatalf("%d candidates unreadable", len(bad))
	}

	var compared, agreed int
	disagreements := map[string]int{}
	for _, c := range all {
		if c.PRSignal == nil || c.Contest == "" {
			continue
		}
		// Only the two verdicts a signal can produce are comparable; no_pr is
		// decided before a signal exists.
		if c.Contest != model.ContestActivePR && c.Contest != model.ContestStalePR {
			continue
		}
		compared++
		got, reasons := ClassifyOne(c.PRSignal, cfg.Policy.Staleness)
		if got == c.Contest {
			agreed++
			continue
		}
		key := fmt.Sprintf("py=%s go=%s", c.Contest, got)
		disagreements[key]++
		if disagreements[key] <= 3 {
			t.Errorf("%s: python said %s, go says %s (%v)\n    signal: commit=%v comment=%v "+
				"changes=%v stale=%v ci=%v",
				c.Slug(), c.Contest, got, reasons,
				d(c.PRSignal.DaysSinceCommit), d(c.PRSignal.DaysSinceAuthorComment),
				d(c.PRSignal.DaysSinceChangesReqested), c.PRSignal.HasStaleLabel,
				c.PRSignal.ChecksFailing)
		}
	}

	fmt.Printf("compared %d stored signals, %d agreed\n", compared, agreed)
	for k, n := range disagreements {
		fmt.Printf("  %d x %s\n", n, k)
	}
	if compared == 0 {
		t.Fatal("no stored signals to compare; this proves nothing")
	}
}

func d(p *int) any {
	if p == nil {
		return "unknown"
	}
	return *p
}
