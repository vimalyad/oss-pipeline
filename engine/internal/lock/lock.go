// Package lock gives the pipeline a single writer.
//
// v1 used a check-then-write pair with no atomicity, so two runs starting in
// the same millisecond could both believe they held it. Here the create is
// O_EXCL, which is atomic, and a stale lock is reclaimed exactly once.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// StaleAfter is how long a lock survives its holder disappearing without
// trace. Long enough that a slow container build is not mistaken for a crash.
const StaleAfter = 4 * time.Hour

// ErrBusy means another run holds the lock. It is not a failure: the caller
// should report it and exit, because the other run is doing the work.
var ErrBusy = errors.New("another run holds the lock")

type holder struct {
	PID  int     `json:"pid"`
	What string  `json:"what"`
	At   float64 `json:"at"`
}

type Lock struct {
	path string
	mine bool
	pid  int
}

func New(root string) *Lock {
	return &Lock{path: filepath.Join(root, "state", "pipeline.lock")}
}

// Acquire takes the lock for `what`, or returns ErrBusy.
func (l *Lock) Acquire(what string) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	if err := l.create(what); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}

	// The file exists. Either a live run holds it, or it was left behind.
	if h, live := l.current(); live {
		return fmt.Errorf("%w: %s (pid %d, started %s)", ErrBusy, h.What, h.PID,
			time.Unix(int64(h.At), 0).Format("15:04:05"))
	}
	// Reclaim, once. Bounded deliberately: two processes both reclaiming the
	// same stale lock in a retry loop would livelock.
	_ = os.Remove(l.path)
	if err := l.create(what); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: lost the race to reclaim a stale lock", ErrBusy)
		}
		return err
	}
	return nil
}

func (l *Lock) create(what string) error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	l.pid = os.Getpid()
	l.mine = true
	return json.NewEncoder(f).Encode(holder{
		PID: l.pid, What: what, At: float64(time.Now().Unix()),
	})
}

// current reports the live holder, if there is one. A lock is not live if the
// file is unparseable, the process is gone, or it has aged past StaleAfter.
func (l *Lock) current() (holder, bool) {
	var h holder
	b, err := os.ReadFile(l.path)
	if err != nil {
		return h, false
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, false
	}
	if !alive(h.PID) {
		return h, false
	}
	if time.Since(time.Unix(int64(h.At), 0)) > StaleAfter {
		return h, false
	}
	return h, true
}

// Release drops the lock, but only if we still hold it -- a run that stole a
// stale lock must not delete the new holder's file.
func (l *Lock) Release() error {
	if !l.mine {
		return nil
	}
	if h, _ := l.current(); h.PID != 0 && h.PID != l.pid {
		return nil
	}
	l.mine = false
	err := os.Remove(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
