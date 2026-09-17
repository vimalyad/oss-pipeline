package halt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHaltBlocksAndReleases(t *testing.T) {
	d := t.TempDir()
	s := New(d)
	if err := s.Check("watch"); err != nil {
		t.Fatalf("not halted, should pass: %v", err)
	}
	if err := s.Engage("maintainer asked us to stop", "phone"); err != nil {
		t.Fatal(err)
	}
	err := s.Check("watch")
	if !errors.Is(err, ErrHalted) {
		t.Fatalf("want ErrHalted, got %v", err)
	}
	if !contains(err.Error(), "maintainer asked us to stop") {
		t.Errorf("the reason must reach the message: %v", err)
	}
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if err := s.Check("watch"); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

// A bare `touch state/HALT` must work: someone stopping a runaway job should
// not have to know the file format.
func TestEmptyFileStillHalts(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "state"), 0o755)
	os.WriteFile(filepath.Join(d, "state", "HALT"), nil, 0o644)
	if err := New(d).Check("daily"); !errors.Is(err, ErrHalted) {
		t.Fatalf("touch must halt; got %v", err)
	}
}

func TestReleaseWhenNotHaltedIsNotAnError(t *testing.T) {
	if err := New(t.TempDir()).Release(); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
