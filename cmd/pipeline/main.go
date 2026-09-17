// Command pipeline is the single binary for the OSS contribution pipeline.
//
// Stages run as one-shots under launchd rather than inside a long-lived
// supervisor: a wedged subprocess stays bounded by process exit, and launchd
// re-runs jobs missed while the laptop was asleep.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vimalyad/osspipeline/internal/halt"
	"github.com/vimalyad/osspipeline/internal/identity"
	"github.com/vimalyad/osspipeline/internal/model"
	"github.com/vimalyad/osspipeline/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	root, err := findRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var code int
	switch os.Args[1] {
	case "doctor":
		code = doctor(root)
	case "halt":
		code = engageHalt(root)
	case "resume":
		code = release(root)
	default:
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `pipeline <command>

  doctor    check stored state, identity config and the kill switch
  halt      stop every scheduled stage (takes a reason)
  resume    lift a halt
`)
}

// findRoot locates the pipeline root by walking up for config/identity.env.
// Deliberately not relative to the executable: the binary may be installed
// anywhere, while the state lives where the user keeps it.
func findRoot() (string, error) {
	if r := os.Getenv("OSSP_ROOT"); r != "" {
		return r, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "config", "identity.env")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no config/identity.env found above the working directory; " +
				"run from the pipeline root or set OSSP_ROOT")
		}
		dir = parent
	}
}

// doctor reports on everything that can be checked without touching the
// network. It exits non-zero if anything is actually wrong, so it can be
// wired into a scheduled run.
func doctor(root string) int {
	problems := 0
	fmt.Printf("root: %s\n\n", root)

	// --- identity -------------------------------------------------------
	id, err := identity.Load(filepath.Join(root, "config", "identity.env"))
	if err != nil {
		fmt.Printf("identity        FAIL  %v\n", err)
		problems++
	} else {
		fmt.Printf("identity        ok    %s <%s>\n", id.Name, id.Email)
		for _, p := range []struct{ what, path string }{
			{"credential helper", id.HelperPath()},
			{"git hooks", id.HooksPath()},
		} {
			if _, err := os.Stat(p.path); err != nil {
				fmt.Printf("  %-14s FAIL  missing: %s\n", p.what, p.path)
				problems++
			}
		}
	}

	// --- kill switch ----------------------------------------------------
	if reason, halted := halt.New(root).Active(); halted {
		fmt.Printf("halt            HALTED  %s\n", firstLine(reason))
	} else {
		fmt.Printf("halt            ok    running\n")
	}

	// --- stored state ---------------------------------------------------
	s := store.New(root)
	cands, bad := s.All()
	fmt.Printf("candidates      %-5s %d loaded, %d unreadable\n",
		okIf(len(bad) == 0), len(cands), len(bad))
	for _, b := range bad {
		fmt.Printf("  FAIL  %s: %v\n", b.Slug, b.Err)
		problems++
	}

	// Validate every recorded history edge against the state machine. v1
	// allowed hand-edited JSON, and three files acquired edges that the
	// table forbids -- invisible until something downstream misbehaved.
	illegal := 0
	for _, c := range cands {
		for i, h := range c.History {
			if h.Forced {
				continue // a sanctioned bypass, recorded as such
			}
			from, to := model.Status(h.From), model.Status(h.To)
			if from == "" || to == "" {
				continue
			}
			if !model.Transitions[from][to] {
				if _, ok := model.ReopenEdges[[2]model.Status{from, to}]; ok {
					fmt.Printf("  WARN  %s history[%d]: %s -> %s is a reopen edge "+
						"but is not marked forced (hand-edited?)\n", c.Slug(), i, from, to)
					continue
				}
				fmt.Printf("  FAIL  %s history[%d]: %s -> %s is not a legal edge\n",
					c.Slug(), i, from, to)
				illegal++
			}
		}
	}
	fmt.Printf("history         %-5s %d illegal edge(s)\n", okIf(illegal == 0), illegal)
	problems += illegal

	byStatus := map[model.Status]int{}
	for _, c := range cands {
		byStatus[c.Status]++
	}
	fmt.Printf("\nby status:\n")
	for _, st := range []model.Status{
		model.StatusProposed, model.StatusApproved, model.StatusImplementing,
		model.StatusPushed, model.StatusPROpen, model.StatusChangesRequested,
		model.StatusUpdating, model.StatusMerged, model.StatusClosed,
		model.StatusRejected, model.StatusAbandoned, model.StatusStale,
	} {
		if n := byStatus[st]; n > 0 {
			fmt.Printf("  %-18s %d\n", st, n)
		}
	}

	fmt.Printf("\n%d problem(s)\n", problems)
	if problems > 0 {
		return 1
	}
	return 0
}

func engageHalt(root string) int {
	reason := "halted from the command line"
	if len(os.Args) > 2 {
		reason = os.Args[2]
	}
	if err := halt.New(root).Engage(reason, "cli"); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("halted:", reason)
	fmt.Println("scheduled stages will now exit immediately. `pipeline resume` to lift.")
	return 0
}

func release(root string) int {
	if err := halt.New(root).Release(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("resumed")
	return 0
}

func okIf(b bool) string {
	if b {
		return "ok"
	}
	return "FAIL"
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
