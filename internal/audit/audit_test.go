package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordAppendsValidJSONL(t *testing.T) {
	d := t.TempDir()
	l := New(d)
	for _, a := range []string{"commit", "pr_open", "auto_fix"} {
		if err := l.Record(a, "acme__widget__1", "detail for "+a); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(filepath.Join(d, "state", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %d is not JSON: %v", n+1, err)
		}
		if e.At == "" || e.Action == "" || e.PID == 0 {
			t.Errorf("line %d is missing fields: %+v", n+1, e)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("wrote %d lines, want 3", n)
	}
}

// The log is append-only: a later write must never truncate an earlier one,
// because it is the only record of what this pipeline did in public.
func TestRecordNeverTruncates(t *testing.T) {
	d := t.TempDir()
	New(d).Record("first", "", "")
	New(d).Record("second", "", "") // a separate Log value, as a new process would be
	b, _ := os.ReadFile(filepath.Join(d, "state", "audit.log"))
	if !strings.Contains(string(b), "first") || !strings.Contains(string(b), "second") {
		t.Fatalf("log lost an entry:\n%s", b)
	}
}
