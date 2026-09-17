package machine

import (
	"context"
	"strings"
	"testing"
)

func macProfile() *Profile {
	return &Profile{
		OS: "macos", Arch: "arm64", GPU: "Apple M5 Pro", TorchMPS: true,
		DockerOK: true, DockerPlatforms: []string{"linux/arm64", "linux/amd64"},
		Capabilities: []Capability{
			CapOSMacOS, CapArchARM64, CapGPUMetal, CapGPUMPS,
			CapDocker, CapContainerARM, CapContainerAMD,
		},
	}
}

func TestInferFindsGPUAndPlatformNeeds(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       Capability
	}{
		{"cuda", "This only reproduces on CUDA with a recent nvcc", CapGPUCUDA},
		{"nvidia", "Fails on NVIDIA A100 but not on CPU", CapGPUCUDA},
		{"mps", "torch.backends.mps returns wrong results for batched svd", CapGPUMPS},
		{"windows", "The path handling breaks on windows-latest", CapOSWindows},
		{"cluster", "Reproduce by creating a kind cluster and running kubectl apply", CapKubeCluster},
		{"multi gpu", "Only happens with multi-gpu DDP training", CapMultiGPU},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Infer(tc.text)
			for _, r := range got {
				if r.Cap == tc.want {
					if r.Evidence == "" {
						t.Error("a requirement must carry evidence")
					}
					return
				}
			}
			t.Fatalf("did not infer %s from %q; got %+v", tc.want, tc.text, got)
		})
	}
}

// Conservative on purpose: inventing a requirement silently discards workable
// issues, which is worse than occasionally attempting one we cannot finish.
func TestInferDoesNotHallucinateRequirements(t *testing.T) {
	for _, text := range []string{
		"The CLI prints the wrong exit code when the config file is missing",
		"Typo in the README installation section",
		"barracuda is misspelled in the docs", // must not match \bcuda\b
		"The compsognathus fixture is stale",  // must not match \bmps\b
	} {
		if got := Infer(text); len(got) != 0 {
			t.Errorf("Infer(%q) invented %+v", text, got)
		}
	}
}

// TestDecideRejectsWhatThisMachineCannotVerify is the whole point: this
// rejection happens before a patch is written.
func TestDecideRejectsWhatThisMachineCannotVerify(t *testing.T) {
	d := Decide(macProfile(), Infer("Crashes on CUDA when the tensor is on an NVIDIA device"))
	if !d.Reject {
		t.Fatalf("a CUDA issue must be rejected on an Apple machine: %+v", d)
	}
	if !strings.Contains(d.Reason, "Apple GPU") {
		t.Errorf("the reason should say what we have instead: %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "evidence") {
		t.Errorf("the reason must be traceable to the thread: %q", d.Reason)
	}
}

// TestDecideRoutesAppleGPUWorkToTheHost is the kornia case. The bug IS
// verifiable here -- on the Mac. What cannot do it is the container.
func TestDecideRoutesAppleGPUWorkToTheHost(t *testing.T) {
	d := Decide(macProfile(), Infer("MPS: torch 2.14 batched linalg.svd fails at >= 8192"))
	if d.Reject {
		t.Fatal("this machine has an Apple GPU; the issue is verifiable here")
	}
	if d.Lane != LaneHost || !d.HostOnly {
		t.Fatalf("Apple GPU work must use the host lane: %+v", d)
	}
	if !strings.Contains(d.Reason, "never eligible for autonomy") {
		t.Errorf("the host lane must state its autonomy exclusion: %q", d.Reason)
	}
}

func TestDecideUsesTheContainerForOrdinaryWork(t *testing.T) {
	d := Decide(macProfile(), Infer("helm template fails on a broken symlink listed in .helmignore"))
	if d.Reject || d.Lane != LaneContainerARM || d.HostOnly {
		t.Fatalf("ordinary work belongs in the container: %+v", d)
	}
}

// A stopped Docker Desktop must block, never silently fall back to the host.
func TestDecideBlocksWhenDockerIsDown(t *testing.T) {
	p := macProfile()
	p.DockerOK = false
	d := Decide(p, nil)
	if d.Lane != LaneNone {
		t.Fatalf("no docker means no lane, not a host fallback: %+v", d)
	}
	if d.Reject {
		t.Error("docker being down is temporary; it must not reject the candidate")
	}
}

func TestDetectDescribesThisMachine(t *testing.T) {
	p := Detect(context.Background())
	if p.OS == "" || p.Arch == "" || p.Cores == 0 {
		t.Fatalf("detection produced nothing useful: %+v", p)
	}
	if len(p.Capabilities) == 0 {
		t.Fatal("no capabilities derived")
	}
	t.Logf("\n%s", p.Summary())
	t.Logf("capabilities: %v", p.Capabilities)
	if p.OS == "macos" && p.MemoryGB == 0 {
		t.Error("memory detection failed on darwin")
	}
}
