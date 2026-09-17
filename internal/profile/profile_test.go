package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func realRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Dir(filepath.Dir(wd))
}

// The shipped config must actually parse. A profile that fails to load stops
// the whole pipeline, so this is worth asserting on the real file.
func TestShippedProfileLoads(t *testing.T) {
	p, err := Load(realRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Domains) == 0 {
		t.Fatal("no domains")
	}
	for _, d := range p.Domains {
		if d.Autonomy != AutonomyOff {
			t.Errorf("domain %s ships with autonomy %q; it must start off",
				d.ID, d.Autonomy)
		}
	}
	t.Logf("%d domains, %d seed repos", len(p.Domains), len(p.SeedRepos()))
}

func TestForRepoPrefersSeedThenLanguageThenTopics(t *testing.T) {
	p := &Profile{User: "u", Domains: []Domain{
		{ID: "cv", Weight: 3, Languages: []string{"Python"},
			Topics: []string{"computer-vision"}, SeedRepos: []string{"kornia/kornia"}},
		{ID: "py", Weight: 1, Languages: []string{"Python"}},
	}}
	if d, ok := p.ForRepo("kornia/kornia", "Python", nil); !ok || d.ID != "cv" {
		t.Fatalf("seed repo should win: %v %v", d.ID, ok)
	}
	if d, ok := p.ForRepo("other/thing", "Python", []string{"computer-vision"}); !ok || d.ID != "cv" {
		t.Fatalf("topic match should beat bare language: %v", d.ID)
	}
	if d, ok := p.ForRepo("other/thing", "Python", nil); !ok || d.ID != "cv" {
		// both match on language alone; higher weight breaks the tie
		t.Fatalf("weight should break the tie: %v", d.ID)
	}
	if _, ok := p.ForRepo("rust/thing", "Rust", nil); ok {
		t.Fatal("a repo outside every domain must not match")
	}
}

func TestAutonomyIsOrdered(t *testing.T) {
	if !AutonomyOpenPRs.AtLeast(AutonomyReply) {
		t.Error("open_prs should permit replying")
	}
	// The ordering that matters: pushing a lint fix is recoverable, posting
	// under the user's name is not.
	if AutonomyFix.AtLeast(AutonomyReply) {
		t.Error("fix must NOT permit replying")
	}
	if !AutonomyShadow.AtLeast(AutonomyShadow) || AutonomyOff.AtLeast(AutonomyShadow) {
		t.Error("shadow/off ordering wrong")
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	for name, p := range map[string]*Profile{
		"no user":      {Domains: []Domain{{ID: "a"}}},
		"no domains":   {User: "u"},
		"no domain id": {User: "u", Domains: []Domain{{}}},
		"duplicate id": {User: "u", Domains: []Domain{{ID: "a"}, {ID: "a"}}},
		"bad autonomy": {User: "u", Domains: []Domain{{ID: "a", Autonomy: "yolo"}}},
	} {
		if err := p.validate(); err == nil {
			t.Errorf("%s: should not validate", name)
		}
	}
}
