package lock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSecondAcquireIsBusy(t *testing.T) {
	d := t.TempDir()
	a, b := New(d), New(d)
	if err := a.Acquire("first"); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire("second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire("second"); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

// TestConcurrentAcquireHasExactlyOneWinner is the reason this package was
// rewritten: v1's check-then-write could hand the lock to two runs at once.
func TestConcurrentAcquireHasExactlyOneWinner(t *testing.T) {
	d := t.TempDir()
	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := New(d)
			<-start
			if err := l.Acquire("race"); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d goroutines acquired the lock; want exactly 1", won)
	}
}

func TestStaleLockIsReclaimed(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "state"), 0o755)
	// A dead pid, timestamped now: the process check alone must reclaim it.
	os.WriteFile(filepath.Join(d, "state", "pipeline.lock"),
		[]byte(`{"pid": 999999, "what": "ghost", "at": 1}`), 0o644)
	if err := New(d).Acquire("live"); err != nil {
		t.Fatalf("a dead holder should be reclaimable: %v", err)
	}
}

func TestReleaseDoesNotStealAnotherHoldersLock(t *testing.T) {
	d := t.TempDir()
	a := New(d)
	if err := a.Acquire("first"); err != nil {
		t.Fatal(err)
	}
	// Simulate another process having taken over the file.
	os.WriteFile(filepath.Join(d, "state", "pipeline.lock"),
		[]byte(`{"pid": 1, "what": "other", "at": 9999999999}`), 0o644)
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "state", "pipeline.lock")); err != nil {
		t.Fatal("release deleted a lock belonging to someone else")
	}
}
