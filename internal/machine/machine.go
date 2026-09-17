// Package machine answers one question: can this computer actually verify a
// fix for this issue?
//
// It exists because of a specific mistake. An Apple-GPU bug was targeted, a
// patch was written, and there was no way to check it except the maintainer's
// CI -- so the PR became a guess. The fix belongs at selection time: if we
// cannot verify the result here, we should not take the issue on.
//
// Capabilities are detected rather than declared. Decisions in this pipeline
// are made from a phone, where the user cannot run sysctl, so asking them to
// supply specifications is not a workable design.
package machine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CacheDays is how long a detected profile is trusted. Hardware does not
// change often; a toolchain install might, hence not "forever".
const CacheDays = 7

// Capability is a token either this machine has or does not.
type Capability string

const (
	CapOSMacOS   Capability = "os:macos"
	CapOSLinux   Capability = "os:linux"
	CapOSWindows Capability = "os:windows"
	CapArchARM64 Capability = "arch:arm64"
	CapArchAMD64 Capability = "arch:amd64"

	// GPU. Metal and MPS are host-only: they cannot be virtualised into a
	// Linux container, which is the whole reason the host lane exists.
	CapGPUMetal Capability = "gpu:metal"
	CapGPUMPS   Capability = "gpu:mps"
	CapGPUCUDA  Capability = "gpu:cuda"
	CapGPUROCm  Capability = "gpu:rocm"

	CapDocker       Capability = "docker"
	CapContainerARM Capability = "container:linux-arm64"
	CapContainerAMD Capability = "container:linux-amd64-emulated"
	CapKubeCluster  Capability = "cluster:k8s"
	CapMultiGPU     Capability = "gpu:multi"
)

// Lane is where a piece of work can run.
type Lane string

const (
	LaneContainerARM Lane = "container:linux/arm64"
	LaneContainerAMD Lane = "container:linux/amd64"
	// LaneHost is the narrow carve-out from "no target code on the host". It
	// exists only for capabilities no container can provide, and it is used
	// for verification runs only -- never for the patch agent.
	LaneHost Lane = "host:macos"
	LaneNone Lane = ""
)

// Profile is what this machine can do. Serialised to state/machine.json.
type Profile struct {
	DetectedAt      string       `json:"detected_at"`
	OS              string       `json:"os"`
	OSVersion       string       `json:"os_version"`
	Arch            string       `json:"arch"`
	CPU             string       `json:"cpu"`
	Cores           int          `json:"cores"`
	MemoryGB        int          `json:"memory_gb"`
	DiskFreeGB      int          `json:"disk_free_gb"`
	GPU             string       `json:"gpu"`
	GPUCores        int          `json:"gpu_cores"`
	DockerOK        bool         `json:"docker_ok"`
	DockerPlatforms []string     `json:"docker_platforms"`
	TorchMPS        bool         `json:"torch_mps"`
	TorchCUDA       bool         `json:"torch_cuda"`
	Capabilities    []Capability `json:"capabilities"`
	Notes           []string     `json:"notes"`
}

func (p *Profile) Has(c Capability) bool {
	for _, got := range p.Capabilities {
		if got == c {
			return true
		}
	}
	return false
}

// Summary is the one-tap confirmation shown to the user.
func (p *Profile) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %d cores · %d GB · %s · %s %s\n",
		p.CPU, p.Cores, p.MemoryGB, p.Arch, p.OS, p.OSVersion)
	if p.GPU != "" {
		fmt.Fprintf(&b, "GPU: %s", p.GPU)
		if p.GPUCores > 0 {
			fmt.Fprintf(&b, ", %d cores", p.GPUCores)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Apple GPU (MPS): %v · CUDA: %v · Docker: %v · %d GB free\n",
		p.TorchMPS, p.TorchCUDA, p.DockerOK, p.DiskFreeGB)
	return b.String()
}

// Detect inspects the machine. Every probe is best-effort: a missing tool
// means a capability is absent, never an error that stops the pipeline.
func Detect(ctx context.Context) *Profile {
	p := &Profile{
		DetectedAt: time.Now().UTC().Format(time.RFC3339),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Cores:      runtime.NumCPU(),
	}
	switch runtime.GOOS {
	case "darwin":
		p.OS = "macos"
		detectDarwin(ctx, p)
	case "linux":
		detectLinux(ctx, p)
	}
	detectDocker(ctx, p)
	detectTorch(ctx, p)
	p.Capabilities = derive(p)
	sort.Slice(p.Capabilities, func(i, j int) bool { return p.Capabilities[i] < p.Capabilities[j] })
	return p
}

func detectDarwin(ctx context.Context, p *Profile) {
	p.CPU = strings.TrimSpace(run(ctx, "sysctl", "-n", "machdep.cpu.brand_string"))
	if b := run(ctx, "sysctl", "-n", "hw.memsize"); b != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(b), 10, 64); err == nil {
			p.MemoryGB = int(n / (1 << 30))
		}
	}
	p.OSVersion = strings.TrimSpace(run(ctx, "sw_vers", "-productVersion"))

	// GPU. system_profiler is slow but it is the only place the core count is.
	out := run(ctx, "system_profiler", "SPDisplaysDataType")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "Chipset Model: "); ok && p.GPU == "" {
			p.GPU = v
		}
		if v, ok := strings.CutPrefix(line, "Total Number of Cores: "); ok && p.GPUCores == 0 {
			p.GPUCores, _ = strconv.Atoi(v)
		}
	}
	p.DiskFreeGB = diskFreeGB(ctx, "/System/Volumes/Data")
}

func detectLinux(ctx context.Context, p *Profile) {
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
				f := strings.Fields(v)
				if len(f) > 0 {
					kb, _ := strconv.ParseInt(f[0], 10, 64)
					p.MemoryGB = int(kb / (1 << 20))
				}
			}
		}
	}
	p.DiskFreeGB = diskFreeGB(ctx, "/")
}

var dfRe = regexp.MustCompile(`(\d+)`)

func diskFreeGB(ctx context.Context, path string) int {
	out := run(ctx, "df", "-g", path)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0
	}
	m := dfRe.FindString(f[3])
	n, _ := strconv.Atoi(m)
	return n
}

func detectDocker(ctx context.Context, p *Profile) {
	// `docker version` on the *server* proves the daemon is up, not just that
	// the client binary exists. A stopped Docker Desktop is the common case.
	if run(ctx, "docker", "version", "--format", "{{.Server.Os}}") == "" {
		p.Notes = append(p.Notes, "docker daemon not reachable; every containerised stage will be blocked")
		return
	}
	p.DockerOK = true
	out := run(ctx, "docker", "buildx", "inspect", "default")
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Platforms:"); ok {
			for _, plat := range strings.Split(v, ",") {
				if plat = strings.TrimSpace(plat); plat != "" {
					p.DockerPlatforms = append(p.DockerPlatforms, plat)
				}
			}
			break
		}
	}
}

// detectTorch asks a Python that already has torch whether the GPU backends
// actually work. Having the hardware is not the same as the toolchain seeing
// it, and that difference is exactly what made one PR unverifiable.
func detectTorch(ctx context.Context, p *Profile) {
	const probe = `import json,torch;print(json.dumps({"mps":torch.backends.mps.is_available(),"cuda":torch.cuda.is_available()}))`
	for _, py := range torchPythons() {
		out := run(ctx, py, "-c", probe)
		var r struct{ MPS, CUDA bool }
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &r) == nil {
			p.TorchMPS, p.TorchCUDA = r.MPS, r.CUDA
			return
		}
	}
}

// torchPythons are interpreters likely to have torch installed. Checked in
// order; the first that answers wins.
func torchPythons() []string {
	var out []string
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, "oss-pipeline", "work", "*", ".venv", "bin", "python"))
	out = append(out, matches...)
	return append(out, "python3")
}

func derive(p *Profile) []Capability {
	var caps []Capability
	add := func(c Capability) { caps = append(caps, c) }

	switch p.OS {
	case "macos":
		add(CapOSMacOS)
	case "linux":
		add(CapOSLinux)
	case "windows":
		add(CapOSWindows)
	}
	switch p.Arch {
	case "arm64":
		add(CapArchARM64)
	case "amd64":
		add(CapArchAMD64)
	}
	if p.OS == "macos" && p.GPU != "" {
		add(CapGPUMetal)
	}
	if p.TorchMPS {
		add(CapGPUMPS)
	}
	if p.TorchCUDA {
		add(CapGPUCUDA)
	}
	if p.DockerOK {
		add(CapDocker)
		for _, plat := range p.DockerPlatforms {
			switch plat {
			case "linux/arm64":
				add(CapContainerARM)
			case "linux/amd64":
				add(CapContainerAMD)
			}
		}
	}
	return caps
}

// Load returns the cached profile, re-detecting if it is missing or stale.
func Load(ctx context.Context, root string) (*Profile, error) {
	path := filepath.Join(root, "state", "machine.json")
	if b, err := os.ReadFile(path); err == nil {
		var p Profile
		if json.Unmarshal(b, &p) == nil {
			if t, err := time.Parse(time.RFC3339, p.DetectedAt); err == nil &&
				time.Since(t) < CacheDays*24*time.Hour {
				return &p, nil
			}
		}
	}
	p := Detect(ctx)
	return p, Save(root, p)
}

func Save(root string, p *Profile) error {
	path := filepath.Join(root, "state", "machine.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func run(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
