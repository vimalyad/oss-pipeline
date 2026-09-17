package machine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Requirement is a capability an issue needs in order to be verified, with
// the evidence that led us to think so. The evidence matters: a rejection
// that cannot be traced back to a phrase in the thread is not reviewable.
type Requirement struct {
	Cap      Capability `json:"cap"`
	Evidence string     `json:"evidence"`
}

// signals map a capability to the phrases that imply it. Word-boundary
// anchored, because "cuda" inside "barracuda" is not a GPU requirement and
// "mps" appears inside plenty of unrelated identifiers.
var signals = []struct {
	cap Capability
	re  *regexp.Regexp
}{
	{CapGPUCUDA, regexp.MustCompile(`(?i)\b(cuda|nvidia|cudnn|nvcc|gpu memory|sm_\d\d|tensor ?core)\b`)},
	{CapGPUROCm, regexp.MustCompile(`(?i)\b(rocm|hip|radeon|amd gpu)\b`)},
	{CapGPUMPS, regexp.MustCompile(`(?i)\b(mps|metal performance shaders|apple silicon gpu|torch\.backends\.mps)\b`)},
	{CapGPUMetal, regexp.MustCompile(`(?i)\b(metal|metalkit|mtl[A-Z])\b`)},
	{CapOSWindows, regexp.MustCompile(`(?i)\b(windows|win32|winapi|powershell|msvc|\.exe\b|windows-latest)\b`)},
	{CapKubeCluster, regexp.MustCompile(`(?i)\b(kubernetes cluster|kind cluster|minikube|k3s|kubectl apply|a running cluster|e2e cluster)\b`)},
	{CapMultiGPU, regexp.MustCompile(`(?i)\b(multi-?gpu|distributed training|ddp\b|nccl|multiple gpus)\b`)},
}

// macOSOnly catches issues that are about macOS itself rather than a GPU.
var macOSOnly = regexp.MustCompile(`(?i)\b(macos|mac os|darwin|osx|macos-latest|apple silicon|m1|m2|m3|m4|m5)\b`)

// Infer reads the issue text and works out what verifying a fix would need.
//
// Deliberately conservative: it only reports a requirement when the text says
// so. Guessing wrong in the "needs CUDA" direction silently discards workable
// issues, which is worse than occasionally attempting one we cannot finish.
func Infer(texts ...string) []Requirement {
	blob := strings.Join(texts, "\n")
	seen := map[Capability]bool{}
	var out []Requirement
	for _, s := range signals {
		if m := s.re.FindString(blob); m != "" && !seen[s.cap] {
			seen[s.cap] = true
			out = append(out, Requirement{Cap: s.cap, Evidence: excerpt(blob, m)})
		}
	}
	// macOS alone implies the host lane only when no GPU signal already did.
	if !seen[CapGPUMPS] && !seen[CapGPUMetal] {
		if m := macOSOnly.FindString(blob); m != "" {
			out = append(out, Requirement{Cap: CapOSMacOS, Evidence: excerpt(blob, m)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cap < out[j].Cap })
	return out
}

func excerpt(blob, match string) string {
	i := strings.Index(blob, match)
	if i < 0 {
		return match
	}
	start, end := max(0, i-40), min(len(blob), i+len(match)+40)
	s := strings.ReplaceAll(blob[start:end], "\n", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// Decision is where (and whether) an issue can be worked on.
type Decision struct {
	Lane     Lane
	Reject   bool   // this machine can never verify it
	Reason   string // always populated: reports must explain themselves
	HostOnly bool   // verification must use the host lane; no autonomy
}

// hostOnly are capabilities that exist on this machine but cannot be passed
// into a Linux container. Metal is not virtualisable; that is a fact about
// Apple's stack, not a limitation we chose.
var hostOnly = map[Capability]bool{
	CapGPUMetal: true, CapGPUMPS: true, CapOSMacOS: true,
}

// Decide chooses a lane for an issue given what it needs and what we have.
func Decide(p *Profile, reqs []Requirement) Decision {
	if p == nil {
		return Decision{Lane: LaneNone, Reject: false,
			Reason: "machine profile not detected yet"}
	}
	if !p.DockerOK {
		return Decision{Lane: LaneNone,
			Reason: "docker is not running; every containerised stage is blocked"}
	}

	var needHost []Requirement
	for _, r := range reqs {
		if p.Has(r.Cap) {
			if hostOnly[r.Cap] {
				needHost = append(needHost, r)
			}
			continue
		}
		// Needed, and we do not have it. This is the rejection that saves the
		// most work, because it happens before a patch is written.
		return Decision{
			Lane:   LaneNone,
			Reject: true,
			Reason: fmt.Sprintf("needs %s, which this machine does not have (%s); evidence: %q",
				r.Cap, describeAbsence(p, r.Cap), r.Evidence),
		}
	}

	if len(needHost) > 0 {
		var caps []string
		for _, r := range needHost {
			caps = append(caps, string(r.Cap))
		}
		return Decision{
			Lane:     LaneHost,
			HostOnly: true,
			Reason: fmt.Sprintf("needs %s, which no Linux container can provide; "+
				"verification runs on the host and this is never eligible for autonomy",
				strings.Join(caps, " and ")),
		}
	}
	return Decision{Lane: LaneContainerARM, Reason: "verifiable in a Linux container"}
}

// describeAbsence says what we have instead, so a rejection tells the user
// something actionable about their hardware rather than just "no".
func describeAbsence(p *Profile, c Capability) string {
	switch c {
	case CapGPUCUDA, CapGPUROCm:
		if p.Has(CapGPUMPS) {
			return "this machine has an Apple GPU, not NVIDIA or AMD"
		}
		return "no discrete GPU detected"
	case CapOSWindows:
		return "this machine is macOS"
	case CapKubeCluster:
		return "no Kubernetes cluster is configured for the pipeline"
	case CapMultiGPU:
		return "a single GPU at most"
	}
	return "not detected"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
