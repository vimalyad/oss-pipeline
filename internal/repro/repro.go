// Package repro reproduces a failure inside a container before anything is
// pushed.
//
// Pushing a guess burns a maintainer's CI minutes and is visible to them, so
// the rule is: take the failing command out of the log, run it here, confirm
// it breaks, fix, confirm it passes, and only then push.
//
// The distinction that makes this worth having is between "their bug happens
// here too" and "our container is wrong". A missing system library, a test
// that wants the network we deliberately removed, or an OOM kill all produce a
// red result that looks exactly like a reproduced defect, and treating one as
// the other means patching code that was never broken. Everything here exists
// to keep those apart.
package repro

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vimalyad/osspipeline/internal/recipe"
	"github.com/vimalyad/osspipeline/internal/sandbox"
)

// runner is the part of a sandbox session this package needs.
type runner interface {
	Run(ctx context.Context, cmd string) (sandbox.Result, error)
}

var _ runner = (*sandbox.Session)(nil)

var (
	// ErrEnvironment means the container could not run the command, so
	// nothing was learned about the code. It is never a reason to patch.
	ErrEnvironment = errors.New("container environment failure")
	ErrRepro       = errors.New("reproduction")
)

// Outcome is what one run told us.
type Outcome string

const (
	// Reproduced: it fails here, for a reason that looks like the code.
	Reproduced Outcome = "reproduced"
	// Passed: it does not fail here. Either the fix works, or the failure is
	// specific to the CI runner and cannot be verified locally.
	Passed Outcome = "passed"
	// Environment: it failed, but for a reason that is ours.
	Environment Outcome = "environment"
)

// Result pairs an outcome with the evidence for it.
type Result struct {
	Outcome  Outcome
	Command  string
	Code     int
	Output   string
	Why      string
	TimedOut bool
}

// envFailures are container problems wearing a failing test's clothes.
//
// The network entries matter most. Verification runs with --network none on
// purpose -- a test that only passes when it can reach the internet is not
// telling us about the patch -- but that same flag makes every network-using
// test fail, and those failures are our constraint rather than their defect.
var envFailures = []struct {
	re  *regexp.Regexp
	why string
}{
	{regexp.MustCompile(`(?i)(temporary failure in name resolution|name or service not known|could not resolve host|nodename nor servname)`),
		"DNS is unavailable: verification runs with the network off"},
	{regexp.MustCompile(`(?i)(connection refused|network is unreachable|no route to host|failed to establish a new connection|max retries exceeded)`),
		"the test needs network access, which verification deliberately removes"},
	{regexp.MustCompile(`(?i)(command not found|: not found\b|no such file or directory: )`),
		"a command the repository expects is not installed in this image"},
	{regexp.MustCompile(`(?i)(error while loading shared libraries|cannot open shared object file|libgl\.so|libglib)`),
		"a system library is missing from this image"},
	{regexp.MustCompile(`(?i)(no space left on device|disk quota exceeded)`),
		"the container ran out of disk"},
	{regexp.MustCompile(`(?i)(killed\s*$|out of memory|oomkill|signal: killed|exit status 137)`),
		"the container was killed, most likely out of memory"},
	{regexp.MustCompile(`(?i)(permission denied)`),
		"a permission problem inside the container, not in the code"},
	{regexp.MustCompile(`(?i)(could not find a version that satisfies|no matching distribution found|failed to solve|error: externally-managed-environment)`),
		"dependency installation failed in this image"},
	{regexp.MustCompile(`(?i)(illegal instruction|exec format error|rosetta|qemu: uncaught)`),
		"the image is the wrong architecture for this machine"},
}

// classify decides whether a failing run says anything about the code.
func classify(res sandbox.Result) (Outcome, string) {
	if res.TimedOut {
		return Environment, "the command hit the sandbox timeout"
	}
	if res.OK() {
		return Passed, ""
	}
	for _, p := range envFailures {
		if p.re.MatchString(res.Output) {
			return Environment, p.why
		}
	}
	return Reproduced, fmt.Sprintf("exit %d", res.Code)
}

// Install runs a recipe's install steps. It must be given a session with the
// network on; nothing else in this package may be.
//
// A failure here is always ErrEnvironment. The repository's dependencies not
// installing says nothing about the patch, and the one thing that must not
// happen is for a broken image to be reported as a broken change.
func Install(ctx context.Context, sess runner, r recipe.Recipe) ([]Result, error) {
	var out []Result
	for _, s := range r.Install {
		res, err := sess.Run(ctx, s.Run)
		if err != nil {
			return out, fmt.Errorf("%w: %s: %v", ErrEnvironment, s.Run, err)
		}
		oc, why := classify(res)
		out = append(out, Result{
			Outcome: oc, Command: s.Run, Code: res.Code,
			Output: res.Output, Why: why, TimedOut: res.TimedOut,
		})
		if !res.OK() {
			if why == "" {
				why = fmt.Sprintf("exit %d", res.Code)
			}
			return out, fmt.Errorf("%w: install step %q: %s", ErrEnvironment, s.Run, why)
		}
	}
	return out, nil
}

// Verify runs a recipe's test commands. The session must have the network off:
// that run is the oracle, and a suite that only passes with network access has
// not verified the patch.
func Verify(ctx context.Context, sess runner, r recipe.Recipe) ([]Result, error) {
	if len(r.Test) == 0 {
		return nil, fmt.Errorf("%w: recipe has no test command", ErrRepro)
	}
	var out []Result
	for _, s := range r.Test {
		res, err := sess.Run(ctx, s.Run)
		if err != nil {
			return out, fmt.Errorf("%w: %s: %v", ErrEnvironment, s.Run, err)
		}
		oc, why := classify(res)
		out = append(out, Result{
			Outcome: oc, Command: s.Run, Code: res.Code,
			Output: res.Output, Why: why, TimedOut: res.TimedOut,
		})
	}
	return out, nil
}

// Confirm runs the command that failed in CI and reports whether it fails the
// same way here. This is the gate before any patch is written: a failure we
// cannot reproduce is one we cannot claim to have fixed.
func Confirm(ctx context.Context, sess runner, command string) (Result, error) {
	res, err := sess.Run(ctx, command)
	if err != nil {
		return Result{Outcome: Environment, Command: command, Why: err.Error()},
			fmt.Errorf("%w: %v", ErrEnvironment, err)
	}
	oc, why := classify(res)
	if oc == Passed {
		why = "the command CI failed on passes here; the failure is specific to " +
			"their runner and cannot be verified locally"
	}
	return Result{
		Outcome: oc, Command: command, Code: res.Code,
		Output: res.Output, Why: why, TimedOut: res.TimedOut,
	}, nil
}

// Attempts is how many fix-and-push rounds a failure earns.
//
// Three when we can watch it break and watch it pass again. One when we
// cannot, because each attempt is then a guess published to a maintainer's
// CI, and the only guess worth making is against a log that names an
// unambiguous defect.
func Attempts(reproduced bool) int {
	if reproduced {
		return 3
	}
	return 1
}

// Failed returns the results that indicate a real failure, ignoring the ones
// caused by the container.
func Failed(rs []Result) []Result {
	var out []Result
	for _, r := range rs {
		if r.Outcome == Reproduced {
			out = append(out, r)
		}
	}
	return out
}

// EnvironmentProblems returns the results the container is responsible for.
// They belong in the digest -- they are a recipe to fix, not a patch to write.
func EnvironmentProblems(rs []Result) []Result {
	var out []Result
	for _, r := range rs {
		if r.Outcome == Environment {
			out = append(out, r)
		}
	}
	return out
}

// Green reports whether every command passed.
func Green(rs []Result) bool {
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r.Outcome != Passed {
			return false
		}
	}
	return true
}

func (r Result) String() string {
	s := fmt.Sprintf("%s: %s", r.Outcome, firstLine(r.Command))
	if r.Why != "" {
		s += " (" + r.Why + ")"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
