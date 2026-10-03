// Package audit is the append-only record of everything this pipeline does
// that changes the outside world, or its own mind about a candidate.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Entry is one line of the log. JSONL, so it can be grepped and tailed.
type Entry struct {
	At     string `json:"at"`
	Action string `json:"action"`
	Slug   string `json:"slug"`
	Detail string `json:"detail"`
	PID    int    `json:"pid"`
}

type Log struct{ path string }

func New(root string) *Log {
	return &Log{path: filepath.Join(root, "state", "audit.log")}
}

// Record appends one entry. Failing to write the audit log must never stop
// the action it describes, so the error is returned for logging, not raised.
func (l *Log) Record(action, slug, detail string) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(Entry{
		At:     time.Now().UTC().Format("2006-01-02T15:04:05-07:00"),
		Action: action, Slug: slug, Detail: detail, PID: os.Getpid(),
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", b)
	return err
}
