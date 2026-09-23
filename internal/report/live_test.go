package report

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/ledger"
	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/policy"
	"github.com/vimalyad/osspipeline/internal/store"
	"github.com/vimalyad/osspipeline/internal/text"
)

// TestLiveRender builds the page from the real state on disk, for comparison
// against the Python implementation still running in production. Offline: it
// exercises the parts that do not need GitHub, which is everything except the
// per-PR detail.
//
//	OSSP_LIVE=1 go test ./internal/report -run TestLiveRender -v
func TestLiveRender(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1 to render from the real state")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	s := store.New(root)
	cfg, err := policy.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	all, bad := s.All()
	if len(bad) > 0 {
		t.Fatalf("%d candidates unreadable", len(bad))
	}

	in := Input{
		Generated: time.Now(),
		Login:     "vimalyad",
		Ledger: ledger.Compute(all, ledger.Unlock{
			MinPRs:    cfg.Policy.Watchlist.UnlockMinPRs,
			MergeRate: cfg.Policy.Watchlist.UnlockMergeRate,
		}, time.Now()),
	}
	for _, c := range s.ByStatus(model.StatusProposed) {
		if len(c.Blockers) > 0 {
			in.NeedsYou = append(in.NeedsYou, Item{Text: fmt.Sprintf("**%s#%d** — %s", c.Repo, c.Issue, text.Clip(c.Blockers[0], 90))})
			continue
		}
		in.NeedsYou = append(in.NeedsYou, Item{
			Text: fmt.Sprintf("**%s#%d** %s — approve or reject", c.Repo, c.Issue, text.Clip(c.Title, 58)),
			Cmd:  "pipeline approve " + c.Slug(),
		})
	}
	for _, c := range s.ByStatus(model.StatusApproved) {
		if len(c.Blockers) > 0 {
			in.NeedsYou = append(in.NeedsYou, Item{Text: fmt.Sprintf("**%s#%d** — %s", c.Repo, c.Issue, text.Clip(c.Blockers[0], 90))})
			continue
		}
		in.Queued = append(in.Queued, Item{
			Text: fmt.Sprintf("**%s#%d** %s", c.Repo, c.Issue, text.Clip(c.Title, 52)),
			Cmd:  "will run on the next scheduled cycle",
		})
	}
	fmt.Println(Render(in))
}
