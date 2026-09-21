// Package sandbox is the only place in this pipeline that executes code
// belonging to a target repository.
//
// Building and testing someone else's project runs that project's scripts. A
// compromised dependency in any watched repository should find nothing worth
// taking, so a sandbox container receives no credential of any kind -- not a
// GitHub token, not a model API key -- and by default no network.
//
// The patch agent stays on the host with execution disabled; it may only read
// and write files in the bind-mounted clone. Every command it would have run
// goes through here instead. That makes the boundary structural rather than a
// policy the agent is asked to respect, and it means each command run against
// a target repository is a logged decision of ours.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

var (
	// ErrUnavailable means Docker is not usable. It is never a reason to fall
	// back to the host: a candidate is blocked instead.
	ErrUnavailable = errors.New("sandbox unavailable")
	ErrSandbox     = errors.New("sandbox")
)

// Result is one command's outcome.
type Result struct {
	Code     int
	Output   string
	Duration time.Duration
	TimedOut bool
	Command  string
}

func (r Result) OK() bool { return r.Code == 0 && !r.TimedOut }

// Limits bound what a container may consume. A target repository's build is
// not trusted to be well behaved.
type Limits struct {
	Memory    string
	CPUs      string
	PIDs      int
	TmpfsMB   int
	Timeout   time.Duration
	TailBytes int
}

func DefaultLimits() Limits {
	return Limits{
		Memory: "8g", CPUs: "6", PIDs: 4096, TmpfsMB: 4096,
		Timeout: 20 * time.Minute, TailBytes: 8000,
	}
}

// Spec describes a session.
type Spec struct {
	Image string
	// Clone is the host path bind-mounted at /work.
	Clone string
	// Network is off unless a step genuinely needs to fetch dependencies.
	// Verification runs with it off: that run is the oracle, and a test that
	// only passes with network access is not telling us about the patch.
	Network bool
	// Volumes are named docker volumes, mounted to warm caches and to keep
	// build output out of the bind-mounted clone.
	Volumes  map[string]string
	Env      map[string]string
	Platform string
	Labels   map[string]string
	Limits   Limits

	// gitConfig is a sanitised copy of the clone's .git/config, mounted over
	// the real one. Set by Start, not by callers, so a caller cannot forget
	// it and no caller can point it somewhere else.
	gitConfig string
}

// Available reports whether the Docker daemon is reachable.
//
// `docker version` on the *server* rather than the client: a stopped Docker
// Desktop still has a working client binary, and that difference decides
// whether every containerised stage is blocked.
func Available(ctx context.Context) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "version",
		"--format", "{{.Server.Os}}/{{.Server.Arch}}").Output()
	if err != nil {
		return false, "docker daemon is not reachable; start Docker Desktop"
	}
	return true, strings.TrimSpace(string(out))
}

// Session is a running container that commands are executed in.
type Session struct {
	ID     string
	Spec   Spec
	Log    func(string)
	closed bool
}

func (s *Session) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(fmt.Sprintf(f, a...))
	}
}

// Start launches a detached container. Detached rather than one container per
// command so a warm dependency install is reused across a test run.
func Start(ctx context.Context, spec Spec) (*Session, error) {
	if ok, why := Available(ctx); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	if spec.Limits == (Limits{}) {
		spec.Limits = DefaultLimits()
	}
	cfg, err := sanitiseGitConfig(spec.Clone)
	if err != nil {
		return nil, err
	}
	spec.gitConfig = cfg

	args := []string{"run", "-d", "--rm=false"}
	args = append(args, dockerFlags(spec)...)
	args = append(args, spec.Image, "sleep", "infinity")

	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%w: start: %s", ErrSandbox, stderrOf(err))
	}
	return &Session{ID: strings.TrimSpace(string(out)), Spec: spec}, nil
}

// dockerFlags builds the run arguments.
//
// Extracted so a test can assert on them directly: the security properties of
// this package are a property of this list, and a test that cannot read it
// cannot check them.
func dockerFlags(spec Spec) []string {
	l := spec.Limits
	args := []string{
		"--user", "1000:1000",
		"--workdir", "/work",
		"--mount", "type=bind,src=" + spec.Clone + ",dst=/work",
	}
	if spec.gitConfig != "" {
		// Read-only: the container must not be able to write host git config,
		// and nothing it legitimately does needs to.
		args = append(args,
			"--mount", "type=bind,src="+spec.gitConfig+",dst=/work/.git/config,readonly")
	}
	args = append(args, []string{
		// Equal memory and swap disables swap: a runaway build should be
		// killed rather than thrash the host.
		"--memory", l.Memory, "--memory-swap", l.Memory,
		"--cpus", l.CPUs,
		"--pids-limit", fmt.Sprint(l.PIDs),
		// `exec` is not redundant. Docker applies rw,noexec,nosuid,nodev to
		// every --tmpfs regardless of the options given, so omitting it
		// leaves /tmp mounted noexec -- and Go and cargo both link test
		// binaries into /tmp and run them from there. The symptom is
		// "fork/exec /tmp/go-build.../pkg.test: permission denied" on every
		// package, which reads like a broken patch rather than a broken
		// mount. nosuid stays.
		"--tmpfs", fmt.Sprintf("/tmp:rw,exec,nosuid,size=%dm", l.TmpfsMB),
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--stop-timeout", "5",
	}...)
	if !spec.Network {
		args = append(args, "--network", "none")
	}
	if spec.Platform != "" {
		args = append(args, "--platform", spec.Platform)
	}
	for _, src := range sortedKeys(spec.Volumes) {
		args = append(args, "--mount", "type=volume,src="+src+",dst="+spec.Volumes[src])
	}
	// Only variables explicitly named in the spec. The host environment is
	// never passed through, which is what keeps credentials out of here.
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	args = append(args, "--label", "ossp.managed=1")
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	return args
}

// Run executes one command inside the session.
func (s *Session) Run(ctx context.Context, cmd string) (Result, error) {
	if s.closed {
		return Result{}, fmt.Errorf("%w: session already closed", ErrSandbox)
	}
	timeout := s.Spec.Limits.Timeout
	if timeout == 0 {
		timeout = DefaultLimits().Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	c := exec.CommandContext(runCtx, "docker", "exec", "-i", s.ID, "/bin/bash", "-lc", cmd)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	res := Result{Output: tail(buf.String(), s.Spec.Limits.TailBytes),
		Duration: time.Since(started), Command: cmd}

	if runCtx.Err() == context.DeadlineExceeded {
		// Killing the local `docker exec` client does not stop the process
		// inside the container. It has to be killed explicitly or it keeps
		// running, holding the session open and consuming the host's CPU.
		s.logf("    timed out after %s; killing the container", timeout)
		s.kill(context.Background())
		res.TimedOut = true
		res.Code = -1
		return res, nil
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Code = ee.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("%w: exec: %v", ErrSandbox, err)
	}
	return res, nil
}

func (s *Session) kill(ctx context.Context) {
	_ = exec.CommandContext(ctx, "docker", "exec", s.ID, "pkill", "-TERM", "-u", "dev").Run()
	time.Sleep(2 * time.Second)
	_ = exec.CommandContext(ctx, "docker", "kill", s.ID).Run()
}

// Close removes the container. Safe to call twice.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.Spec.gitConfig != "" {
		// Removed even if the container teardown below fails: leaving a
		// stray file in someone's .git directory is our mess, not Docker's.
		_ = os.Remove(s.Spec.gitConfig)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "rm", "-f", s.ID).Run()
}

// Sweep removes containers this pipeline left behind. A run killed by the
// scheduler does not get to run its deferred cleanup, so the next run does it.
func Sweep(ctx context.Context) (int, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=ossp.managed=1").Output()
	if err != nil {
		return 0, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return 0, nil
	}
	args := append([]string{"rm", "-f"}, ids...)
	return len(ids), exec.CommandContext(ctx, "docker", args...).Run()
}

func tail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return "... (truncated) ...\n" + s[len(s)-n:]
}

func stderrOf(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

// sortedKeys gives a deterministic flag order. Go randomises map iteration, so
// without this the same spec produces different arguments run to run -- which
// makes the tests that assert on those arguments flaky, and those tests are
// how the security properties of this package are checked.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
