package watch

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/vimalyad/osspipeline/internal/cilog"
	"github.com/vimalyad/osspipeline/internal/ghx"
	"github.com/vimalyad/osspipeline/internal/identity"
	"github.com/vimalyad/osspipeline/internal/store"
)

// TestLiveParity polls the real pull requests this pipeline has opened and
// prints what the watcher makes of them, for comparison against the Python
// implementation still running in production. It is the parity gate for this
// group and it is read-only: Poll never transitions anything.
//
//	OSSP_LIVE=1 go test ./internal/watch -run TestLiveParity -v
func TestLiveParity(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1 to poll the real pull requests")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	id, err := identity.Load(root + "/config/identity.env")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := identity.Token(id)
	if err != nil {
		t.Fatal(err)
	}
	cl := ghx.New(identity.Env(id, tok))

	st0 := store.New(root)
	for _, slug := range []string{"kornia__kornia__4201", "cli__cli__14386"} {
		c, err := st0.Load(slug)
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		tc := struct {
			repo string
			pr   int
		}{c.Repo, *c.PRNumber}
		st, err := Poll(context.Background(), cl, c)
		if err != nil {
			t.Fatalf("%s: %v", tc.repo, err)
		}
		fmt.Printf("\n=== %s#%d (watch_seen=%d)\n", tc.repo, tc.pr, len(c.WatchSeen))
		fmt.Printf("state=%s merged=%v draft=%v head=%s updated=%s\n",
			st.State, st.Merged, st.IsDraft, st.HeadSHA[:min(8, len(st.HeadSHA))], st.UpdatedAt)
		fmt.Printf("failing=%d awaiting=%d unseen items=%d\n",
			len(st.Failing), len(st.Awaiting), len(st.Items))
		for _, f := range st.Failing {
			fmt.Printf("  FAIL %s\n", f.Name)
		}
		if len(st.Failing) > 0 {
			f := &cilog.Fetcher{API: cilogAPI{cl}, Repo: tc.repo}
			ClassifyChecks(context.Background(), f, &st, map[string]bool{})
			for _, it := range st.Items {
				if it.Author == "CI" {
					fmt.Printf("  -> %s: %s\n", it.Class, it.Why)
				}
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cilogAPI adapts the gh client to cilog's own options type. cilog declares
// its transport needs rather than importing the client, so the adapter belongs
// at the wiring layer -- here, and in cmd/pipeline.
type cilogAPI struct{ c *ghx.Client }

func (a cilogAPI) REST(ctx context.Context, path string, o cilog.RESTOptions) (string, error) {
	return a.c.REST(ctx, path, ghx.RESTOptions{
		Method: o.Method, Paginate: o.Paginate, JQ: o.JQ, Fields: o.Fields,
	})
}
