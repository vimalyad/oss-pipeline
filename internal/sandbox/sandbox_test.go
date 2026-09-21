package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func spec() Spec {
	return Spec{
		Image: "ossp-gate:test", Clone: "/host/clone",
		Volumes: map[string]string{"ossp-cache-go": "/cache"},
		Limits:  DefaultLimits(),
	}
}

func flagString(s Spec) string { return strings.Join(dockerFlags(s), " ") }

// TestFlagsCarryNoCredential is the security property this package exists for.
// The host environment is never passed through, so a credential in the shell
// cannot reach a target repository's build.
func TestFlagsCarryNoCredential(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_sentinel_must_not_appear")
	t.Setenv("OPENAI_API_KEY", "sk-sentinel-must-not-appear")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-sentinel")

	got := flagString(spec())
	for _, sentinel := range []string{
		"ghp_sentinel_must_not_appear", "sk-sentinel-must-not-appear", "sk-ant-sentinel",
	} {
		if strings.Contains(got, sentinel) {
			t.Fatalf("a host credential reached the container flags: %q", sentinel)
		}
	}
	if strings.Contains(got, "--env-file") || strings.Contains(got, "-e ") {
		t.Errorf("no bulk environment passthrough is permitted: %s", got)
	}
}

// Only variables the caller names explicitly are passed.
func TestOnlyExplicitEnvIsPassed(t *testing.T) {
	s := spec()
	s.Env = map[string]string{"CI": "1", "GOFLAGS": "-mod=mod"}
	got := flagString(s)
	if !strings.Contains(got, "--env CI=1") || !strings.Contains(got, "--env GOFLAGS=-mod=mod") {
		t.Fatalf("explicit env missing: %s", got)
	}
}

func TestNetworkIsOffByDefault(t *testing.T) {
	if !strings.Contains(flagString(spec()), "--network none") {
		t.Fatal("verification must run without network; it is the oracle")
	}
	s := spec()
	s.Network = true
	if strings.Contains(flagString(s), "--network none") {
		t.Fatal("an install step that asked for network did not get it")
	}
}

func TestHardeningFlagsArePresent(t *testing.T) {
	got := flagString(spec())
	for _, want := range []string{
		"--user 1000:1000", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--pids-limit", "--memory 8g", "--memory-swap 8g", "--cpus 6",
		"--label ossp.managed=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// /tmp must be nosuid but NOT noexec: Go and cargo execute test binaries out
// of /tmp, and noexec breaks them in a way that reads like a broken patch.
func TestTmpfsIsExecutable(t *testing.T) {
	got := flagString(spec())
	if !strings.Contains(got, "/tmp:rw,nosuid,size=") {
		t.Fatalf("tmpfs flag wrong: %s", got)
	}
	if strings.Contains(got, "noexec") {
		t.Fatal("noexec on /tmp breaks go and cargo test binaries")
	}
}

// Everything is labelled so pruning can never touch the unrelated Docker
// objects already on this machine.
func TestEverythingIsLabelled(t *testing.T) {
	if !strings.Contains(flagString(spec()), "--label ossp.managed=1") {
		t.Fatal("unlabelled objects cannot be pruned safely")
	}
}

// Go randomises map iteration; without sorting, the same spec produces
// different flags run to run and these tests become flaky.
func TestFlagOrderIsDeterministic(t *testing.T) {
	s := spec()
	s.Volumes = map[string]string{"a": "/a", "b": "/b", "c": "/c", "d": "/d"}
	s.Env = map[string]string{"X": "1", "Y": "2", "Z": "3"}
	first := flagString(s)
	for i := 0; i < 20; i++ {
		if got := flagString(s); got != first {
			t.Fatalf("flags differ between calls:\n%s\n%s", first, got)
		}
	}
}

func TestResultOK(t *testing.T) {
	if !(Result{Code: 0}).OK() {
		t.Error("exit 0 is success")
	}
	if (Result{Code: 0, TimedOut: true}).OK() {
		t.Error("a timeout is not success even at exit 0")
	}
}

func TestTailBoundsOutput(t *testing.T) {
	long := strings.Repeat("x", 10000)
	got := tail(long, 100)
	if len(got) > 200 {
		t.Fatalf("tail did not bound output: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncation should be visible in the output")
	}
}

// --- integration: only with -tags docker ---------------------------------

func dockerAvailable(t *testing.T) {
	t.Helper()
	if os.Getenv("OSSP_DOCKER_TESTS") == "" {
		t.Skip("set OSSP_DOCKER_TESTS=1 to run container integration tests")
	}
	if ok, why := Available(context.Background()); !ok {
		t.Skipf("docker not available: %s", why)
	}
}

func TestIntegrationCredentialIsolation(t *testing.T) {
	dockerAvailable(t)
	t.Setenv("GH_TOKEN", "ghp_sentinel_isolation_check_xxxxxxxx")
	dir := t.TempDir()

	s, err := Start(context.Background(), Spec{
		Image: "ossp-gate:test", Clone: dir, Limits: DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "env; cat /proc/self/environ | tr '\\0' '\\n'")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "sentinel_isolation_check") {
		t.Fatal("a host credential was visible inside the container")
	}
	insp, _ := exec.Command("docker", "inspect", s.ID, "--format", "{{json .Config.Env}}").Output()
	if strings.Contains(string(insp), "sentinel_isolation_check") {
		t.Fatal("the credential is visible in docker inspect")
	}
}

func TestIntegrationOwnershipRoundTrip(t *testing.T) {
	dockerAvailable(t)
	dir := t.TempDir()
	s, err := Start(context.Background(), Spec{
		Image: "ossp-gate:test", Clone: dir, Limits: DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Run(context.Background(), "touch /work/canary"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "canary"))
	if err != nil {
		t.Fatalf("the file did not reach the host: %v", err)
	}
	if !st.Mode().IsRegular() {
		t.Fatal("unexpected file type")
	}
}

func TestIntegrationNetworkIsBlocked(t *testing.T) {
	dockerAvailable(t)
	s, err := Start(context.Background(), Spec{
		Image: "ossp-gate:test", Clone: t.TempDir(), Limits: DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, _ := s.Run(context.Background(), "getent hosts api.github.com")
	if res.Code == 0 {
		t.Fatal("DNS resolved inside a --network none container")
	}
}

func TestIntegrationTimeoutKillsTheProcess(t *testing.T) {
	dockerAvailable(t)
	lim := DefaultLimits()
	lim.Timeout = 3 * time.Second
	s, err := Start(context.Background(), Spec{
		Image: "ossp-gate:test", Clone: t.TempDir(), Limits: lim,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "sleep 60")
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("a command past its timeout must be reported as timed out")
	}
}
