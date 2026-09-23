package repofacts

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/vimalyad/osspipeline/internal/ghx"
	"github.com/vimalyad/osspipeline/internal/identity"
	"github.com/vimalyad/osspipeline/internal/llm"
	"github.com/vimalyad/osspipeline/internal/store"
)

type liveAPI struct{ c *ghx.Client }

func (p liveAPI) Get(ctx context.Context, path string) (string, error) {
	return p.c.REST(ctx, path, ghx.RESTOptions{})
}
func (p liveAPI) GraphQL(ctx context.Context, q string, vars map[string]any, v any) error {
	return p.c.GraphQL(ctx, q, vars, v)
}

// TestLiveAgainstCachedFacts refetches a repository and compares the result
// with what the Python implementation wrote, which is the parity gate for this
// package. It never writes the cache: the comparison is the point, and
// overwriting the file being compared against would make the next run vacuous.
//
//	OSSP_LIVE=1 go test ./internal/repofacts -run TestLive -v
func TestLiveAgainstCachedFacts(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1")
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
	env := identity.Env(id, tok)
	api := liveAPI{ghx.New(env)}
	j := llm.New(env)
	s := store.New(root)

	for _, repo := range []string{"cli/cli", "kornia/kornia", "helm/helm"} {
		want, err := s.LoadRepoFacts(repo)
		if err != nil {
			t.Logf("%s: no cached facts to compare against (%v)", repo, err)
			continue
		}
		got, err := Fetch(context.Background(), api, j, nil, repo, Options{Refresh: true})
		if err != nil {
			t.Errorf("%s: %v", repo, err)
			continue
		}
		fmt.Printf("\n=== %s\n", repo)
		cmp := func(field string, a, b any) {
			mark := "  "
			if fmt.Sprint(a) != fmt.Sprint(b) {
				mark = "??"
			}
			fmt.Printf("%s %-24s go=%v  py=%v\n", mark, field, a, b)
		}
		cmp("stars", got.Stars, want.Stars)
		cmp("primary_language", got.PrimaryLanguage, want.PrimaryLanguage)
		cmp("has_contributing", got.HasContributing, want.HasContributing)
		cmp("has_tests", got.HasTests, want.HasTests)
		cmp("requires_dco", got.RequiresDCO, want.RequiresDCO)
		cmp("requires_cla", got.RequiresCLA, want.RequiresCLA)
		cmp("bans_ai_prs", got.BansAIPRs, want.BansAIPRs)
		cmp("requires_ai_disclosure", got.RequiresAIDisclosure, want.RequiresAIDisclosure)
		cmp("required_issue_labels", got.RequiredIssueLabels, want.RequiredIssueLabels)
		cmp("forbidden_issue_labels", got.ForbiddenIssueLabels, want.ForbiddenIssueLabels)
		cmp("merged_first_time_pr", got.MergedFirstTimePR90d, want.MergedFirstTimePR90d)
		fmt.Printf("   topics (new in go)     %v\n", got.Topics)
	}
}
