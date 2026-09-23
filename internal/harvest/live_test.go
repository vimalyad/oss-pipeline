package harvest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRendersPythonsPayloads is the parity gate, and it needs no network: the
// Python implementation has already harvested 99 threads, so the test is
// whether Go reads those files unchanged and renders the same transcript.
//
// Compared against Python's own render_thread by the companion script; what
// this asserts directly is that every payload loads, every one produces a
// transcript, and the parts a brief depends on survive.
//
//	OSSP_LIVE=1 go test ./internal/harvest -run TestRendersPythons -v
func TestRendersPythonsPayloads(t *testing.T) {
	if os.Getenv("OSSP_LIVE") == "" {
		t.Skip("set OSSP_LIVE=1 to read the real payloads")
	}
	root := "/Users/vimalkumaryadav/oss-pipeline"
	dir := filepath.Join(root, "state", "context")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var loaded, withPRs, withAssoc int
	var totalChars int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".raw.json") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".raw.json")
		p, err := Load(root, slug)
		if err != nil {
			t.Errorf("%s: %v", slug, err)
			continue
		}
		loaded++
		out := Render(p, 0)
		if out == "" {
			t.Errorf("%s: rendered nothing", slug)
			continue
		}
		totalChars += len(out)
		if len(p.LinkedPRs) > 0 {
			withPRs++
		}
		// The association is the thing a brief cannot do without.
		if strings.Contains(out, "[OWNER]") || strings.Contains(out, "[MEMBER]") ||
			strings.Contains(out, "[COLLABORATOR]") {
			withAssoc++
		}
		if !strings.HasPrefix(out, "REPO: ") {
			t.Errorf("%s: transcript does not start with the repo line", slug)
		}
	}

	fmt.Printf("loaded %d payloads written by the python implementation\n", loaded)
	fmt.Printf("  %d have linked pull requests\n", withPRs)
	fmt.Printf("  %d carry an owner/member/collaborator statement\n", withAssoc)
	fmt.Printf("  %d chars of transcript in total (avg %d)\n", totalChars, totalChars/max(1, loaded))
	if loaded == 0 {
		t.Fatal("no payloads loaded; this proves nothing")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
