package guard

import (
	"strings"
	"testing"
)

// TestCommittedAgentFileIsCaught is the regression test for the incident this
// package exists for. A 92-line agent workflow note reached a public PR
// because the check ran against uncommitted changes only: the file had been
// committed already, so the scope was empty and everything looked clean.
func TestCommittedAgentFileIsCaught(t *testing.T) {
	committed := "A\t.agents/skills/kornia-developer/SKILL.md\nM\tkornia/core/utils.go"
	files := NetFiles(committed, "")
	if len(files) != 2 {
		t.Fatalf("net files = %v", files)
	}
	probs := Inspect(Shipment{Files: files, Diff: "some diff",
		ExistingTopLevel: map[string]bool{"kornia": true}})
	var got string
	for _, p := range probs {
		got += p.Why + "\n"
	}
	if !strings.Contains(got, "agent tooling") {
		t.Fatalf("a committed agent file must be caught:\n%s", got)
	}
}

// The other half of the same fix: a file being deleted must stop blocking, or
// the guard refuses the very commit that removes the offending file.
func TestDeletedFileNoLongerBlocks(t *testing.T) {
	committed := "A\t.agents/skills/x/SKILL.md\nM\tsrc/thing.go"
	pending := "D\t.agents/skills/x/SKILL.md"
	files := NetFiles(committed, pending)
	for _, f := range files {
		if strings.Contains(f, ".agents") {
			t.Fatalf("a removed file must leave the shipped set: %v", files)
		}
	}
	if len(Inspect(Shipment{Files: files, Diff: "d",
		ExistingTopLevel: map[string]bool{"src": true}})) != 0 {
		t.Error("removing the offending file should make the branch shippable")
	}
}

func TestNetFilesHandlesRenamesAndOrder(t *testing.T) {
	// Renames carry both names; the destination is what ships.
	files := NetFiles("R100\told/path.go\tnew/path.go", "")
	if len(files) != 1 || files[0] != "new/path.go" {
		t.Fatalf("rename resolved to %v", files)
	}
	// Order of git's output must not change the answer.
	a := NetFiles("A\tone.go\nA\ttwo.go", "D\tone.go")
	b := NetFiles("A\ttwo.go\nA\tone.go", "D\tone.go")
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("order-dependent: %v vs %v", a, b)
	}
}

func TestInspectCatchesEachCategory(t *testing.T) {
	for _, tc := range []struct{ name, path, want string }{
		{"workflow", ".github/workflows/ci.yml", "workflow changes are excluded"},
		{"agent dir", ".claude/settings.json", "agent tooling"},
		{"agent file", "AGENTS.md", "agent tooling"},
		{"cursor rules", ".cursorrules.md", "agent tooling"},
		{"pycache", "src/__pycache__/x.pyc", "build artefact"},
		{"node_modules", "web/node_modules/pkg/index.js", "build artefact"},
		{"our tooling", "uv.lock", "our own test tooling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probs := Inspect(Shipment{Files: []string{tc.path}, Diff: "d",
				ExistingTopLevel: map[string]bool{
					".github": true, ".claude": true, "src": true, "web": true}})
			var joined string
			for _, p := range probs {
				joined += p.Why + "\n"
			}
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("%s not caught; got:\n%s", tc.path, joined)
			}
		})
	}
}

// Ordinary files must pass, or the guard is useless noise.
func TestOrdinaryChangesPass(t *testing.T) {
	files := []string{"src/thing.go", "src/thing_test.go", "docs/changelog.d/123.fixed.md"}
	probs := Inspect(Shipment{Files: files, Diff: "real diff",
		ExistingTopLevel: map[string]bool{"src": true, "docs": true}})
	if len(probs) != 0 {
		t.Fatalf("false positives: %v", probs)
	}
}

func TestNewTopLevelDirIsRefused(t *testing.T) {
	probs := Inspect(Shipment{
		Files:            []string{"scratch/notes.md", "src/ok.go"},
		Diff:             "d",
		ExistingTopLevel: map[string]bool{"src": true},
	})
	var joined string
	for _, p := range probs {
		joined += p.Why
	}
	if !strings.Contains(joined, "new top-level directory") {
		t.Fatalf("got %s", joined)
	}
}

func TestScanSecrets(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"github token", "token = ghp_" + strings.Repeat("a", 30), "GitHub token"},
		{"fine grained", "github_pat_" + strings.Repeat("b", 30), "GitHub fine-grained token"},
		{"aws", "AKIA" + strings.Repeat("A", 16), "AWS access key"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----", "private key"},
		{"anthropic", "sk-ant-" + strings.Repeat("c", 25), "Anthropic API key"},
		{"slack", "xoxb-1234567890-abc", "Slack token"},
		{"work email", "contact someone@private.example", "work email address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanSecrets(tc.text)
			if len(got) == 0 {
				t.Fatalf("missed %s", tc.name)
			}
			var found bool
			for _, g := range got {
				if g == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("got %v want %s", got, tc.want)
			}
		})
	}
	if got := ScanSecrets("ordinary code with no secrets"); len(got) != 0 {
		t.Errorf("false positive: %v", got)
	}
}

// The PR body is read by maintainers. None of this vocabulary exists to them.
func TestCheckBodyCatchesTranscriptLanguage(t *testing.T) {
	body := "I could not run the tests because the sandbox denied the command. " +
		"Claude statically reviewed the diff instead."
	got := CheckBody(body)
	for _, want := range []string{"claude", "sandbox", "i could not", "statically reviewed"} {
		var found bool
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missed %q in %v", want, got)
		}
	}
	clean := "Guards the empty-config case and adds a regression test."
	if got := CheckBody(clean); len(got) != 0 {
		t.Errorf("false positive on a normal body: %v", got)
	}
}
