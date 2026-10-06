package recipe

import (
	"strings"
	"testing"
)

// typescript-eslint installs pnpm, Node and its dependencies inside a local
// composite action. Read as one opaque action, the recipe had none of the
// three, and the first autonomous approval died at `pnpm build: a command the
// repository expects is not installed in this image`.
func TestALocalCompositeActionIsReadAsItsSteps(t *testing.T) {
	r, err := Resolve(fixture(t, "typescript-eslint__typescript-eslint"),
		"typescript-eslint/typescript-eslint", "TypeScript", nil, tc)
	if err != nil && r.BaseImage == "" {
		t.Fatalf("no recipe: %v", err)
	}
	var install []string
	for _, s := range r.Install {
		install = append(install, s.Run)
	}
	joined := strings.Join(install, "\n")
	pnpmAt := strings.Index(joined, "npm install -g pnpm@12.8.0")
	installAt := strings.Index(joined, "pnpm install --frozen-lockfile")
	if pnpmAt < 0 {
		t.Fatalf("pnpm is never installed, at the packageManager version:\n%s", joined)
	}
	if installAt < 0 || installAt < pnpmAt {
		t.Fatalf("the action's dependency install is missing or runs before pnpm exists:\n%s", joined)
	}
	if !strings.HasPrefix(r.BaseImage, "node:24") {
		t.Errorf("base image %q, want Node 24 from env.PRIMARY_NODE_VERSION", r.BaseImage)
	}
	for _, u := range r.Unresolved {
		if strings.Contains(u, "prepare-install") {
			t.Errorf("the local action is still reported unhandled: %s", u)
		}
	}
}
