package brief

import (
	"fmt"
	"os"
	"testing"

	"github.com/vimalyad/osspipeline/internal/harvest"
	"github.com/vimalyad/osspipeline/internal/store"
	"github.com/vimalyad/osspipeline/internal/text"
)

// TestStoredBriefsQuoteTheirThreads audits what the Python implementation has
// already extracted. Every candidate with a brief also has the thread it was
// extracted from on disk, so the verbatim-quote rule can be checked after the
// fact against real answers from a real model.
//
// It is not a pass/fail gate on the port. It answers a question worth knowing:
// has the model ever recorded a maintainer approach that is not in the thread?
//
//	OSSP_LIVE=1 go test ./internal/brief -run TestStoredBriefs -v
func TestStoredBriefsQuoteTheirThreads(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1 to audit the stored briefs")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	all, bad := store.New(root).All()
	if len(bad) > 0 {
		t.Fatalf("%d candidates unreadable", len(bad))
	}

	var checked, withApproach, verified, unverifiable int
	var rejectedChecked, rejectedVerified int
	for _, c := range all {
		if c.Brief == nil {
			continue
		}
		p, err := harvest.Load(root, c.Slug())
		if err != nil {
			continue // harvested payload pruned or never written
		}
		transcript := harvest.Render(p, 0)
		if transcript == "" {
			continue
		}
		checked++

		if q := c.Brief.MaintainerDesiredApproach; q != "" {
			withApproach++
			if quoted(transcript, q) {
				verified++
			} else {
				unverifiable++
				t.Logf("NOT FOUND IN THREAD  %s\n    assoc=%s\n    quote=%q",
					c.Slug(), c.Brief.ApproachAuthorAssociation, text.Ellipsis(q, 200))
			}
		}
		// rejected_approaches are deliberately rewritten by rule 3, so they
		// are counted only to show how many would have been wrongly
		// discarded had the verbatim check been applied to them.
		for _, q := range c.Brief.RejectedApproaches {
			rejectedChecked++
			if quoted(transcript, q) {
				rejectedVerified++
			}
		}
	}

	fmt.Printf("briefs with a harvested thread: %d\n", checked)
	fmt.Printf("  maintainer approaches: %d, of which %d quote the thread and %d do not\n",
		withApproach, verified, unverifiable)
	fmt.Printf("  rejected approaches:   %d, of which %d quote the thread\n",
		rejectedChecked, rejectedVerified)
	if checked == 0 {
		t.Fatal("nothing to audit; this proves nothing")
	}
}
