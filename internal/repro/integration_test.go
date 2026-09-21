package repro

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vimalyad/osspipeline/internal/image"
	"github.com/vimalyad/osspipeline/internal/recipe"
	"github.com/vimalyad/osspipeline/internal/sandbox"
	"github.com/vimalyad/osspipeline/internal/toolchain"
)

// TestIntegrationWholeCycleOnARealClone is the parity gate for this group. It
// runs the path a candidate actually takes -- resolve a recipe, build its
// image, install with the network on, detach, verify with it off -- against a
// real checkout, because every defect found while building this group was
// found by running it and none by reading it.
//
//	OSSP_DOCKER_TESTS=1 \
//	OSSP_E2E_CLONE=~/oss-pipeline/work/kornia__kornia \
//	OSSP_E2E_REPO=kornia/kornia OSSP_E2E_LANG=Python \
//	OSSP_E2E_TEST='pytest -q tests/geometry/test_homography.py' \
//	go test ./internal/repro -run TestIntegrationWholeCycle -v
//
// OSSP_E2E_TEST narrows the suite so the test finishes in minutes; everything
// else is the recipe the pipeline would really use.
func TestIntegrationWholeCycleOnARealClone(t *testing.T) {
	if os.Getenv("OSSP_DOCKER_TESTS") == "" {
		t.Skip("set OSSP_DOCKER_TESTS=1")
	}
	clone, repo := os.Getenv("OSSP_E2E_CLONE"), os.Getenv("OSSP_E2E_REPO")
	if clone == "" || repo == "" {
		t.Skip("set OSSP_E2E_CLONE and OSSP_E2E_REPO")
	}
	lang := os.Getenv("OSSP_E2E_LANG")

	overrides, err := recipe.LoadOverrides(os.Getenv("OSSP_E2E_OVERRIDES"))
	if err != nil {
		t.Fatal(err)
	}
	var ov *recipe.Override
	if o, ok := overrides[repo]; ok {
		ov = &o
	}
	tc := recipe.CommandsFunc(func(root string) (test, lint, install, masks []string) {
		for _, t := range toolchain.Detect(root) {
			test, lint, install = append(test, t.Test...), append(lint, t.Lint...), append(install, t.Install...)
		}
		return
	})

	r, err := recipe.Resolve(clone, repo, lang, ov, tc)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Logf("recipe: %s", r)
	if scoped := os.Getenv("OSSP_E2E_TEST"); scoped != "" {
		r.Test = []recipe.Step{{Kind: "test", Run: scoped, From: "OSSP_E2E_TEST"}}
	}

	ctx := context.Background()
	built, err := image.Ensure(ctx, r)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Logf("image: %s (cached=%v, %s)", built.Tag, built.Cached, built.Duration.Round(time.Second))

	lim := sandbox.DefaultLimits()
	lim.Timeout = 40 * time.Minute
	s, err := sandbox.Start(ctx, sandbox.Spec{
		Image: built.Tag, Clone: clone, Network: true,
		Volumes: r.Volumes(), Platform: r.Platform, Limits: lim,
		Labels: map[string]string{"ossp.repo": repo},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Close()

	installed, err := Install(ctx, s, r)
	for _, res := range installed {
		t.Logf("install %s", res)
	}
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := s.Detach(ctx); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if !s.Networkless() {
		t.Fatal("the verification run still has a network")
	}

	results, err := Verify(ctx, s, r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	for _, res := range results {
		t.Logf("verify %s", res)
		if res.Outcome != Passed {
			t.Logf("output tail:\n%s", lastLines(res.Output, 25))
		}
	}
	// An environment verdict here is a defect in the recipe, not in the
	// repository, and it is the one outcome that must never be reported as a
	// failing test.
	if n := len(EnvironmentProblems(results)); n > 0 {
		t.Errorf("%d command(s) failed for reasons belonging to the container", n)
	}
	if !Green(results) {
		t.Errorf("the checkout does not pass its own tests in this container")
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
