// Package halt is the kill switch.
//
// v1's README documented `touch state/HALT` as the way to stop everything.
// It was never implemented -- creating the file did nothing, and the only
// real way to stop an unattended run that was pushing commits was to unload
// the launchd agents. This is that switch, actually wired up.
package halt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrHalted is returned by Check when the pipeline is stopped. Callers should
// exit cleanly rather than treating it as a failure.
var ErrHalted = errors.New("pipeline halted")

type Switch struct{ path string }

func New(root string) *Switch {
	return &Switch{path: filepath.Join(root, "state", "HALT")}
}

// Active returns the reason the pipeline is halted, or "" if it is running.
// Any file at the path halts, whatever it contains: someone in a hurry should
// be able to `touch` it and have that work.
func (s *Switch) Active() (string, bool) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return "", false
	}
	reason := strings.TrimSpace(string(b))
	if reason == "" {
		reason = "no reason recorded"
	}
	return reason, true
}

// Check is called at the top of every mutating stage.
func (s *Switch) Check(stage string) error {
	if reason, halted := s.Active(); halted {
		return fmt.Errorf("%w: %s not run -- %s", ErrHalted, stage, reason)
	}
	return nil
}

func (s *Switch) Engage(reason, by string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("%s\nhalted by %s at %s\n", reason, by,
		time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(s.path, []byte(body), 0o644)
}

func (s *Switch) Release() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
