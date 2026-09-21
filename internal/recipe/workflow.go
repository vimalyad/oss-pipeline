package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A note on the `on:` key. Under YAML 1.1 -- which pyyaml implements, and
// which the previous version of this pipeline tripped over -- the bare word
// `on` resolves to the boolean true, so a workflow's triggers arrive under the
// key `True` and every lookup of "on" misses. gopkg.in/yaml.v3 uses the YAML
// 1.2 core schema, where only true/false are booleans, so `on` stays a string.
// TestOnKeyIsAStringNotABoolean pins that, because the bug returns silently if
// the YAML library is ever swapped.

type workflow struct {
	Path string
	Name string         `yaml:"name"`
	On   yaml.Node      `yaml:"on"`
	Jobs map[string]job `yaml:"jobs"`
}

type job struct {
	RunsOn    yaml.Node      `yaml:"runs-on"`
	Container yaml.Node      `yaml:"container"`
	Uses      string         `yaml:"uses"`
	With      map[string]any `yaml:"with"`
	Env       map[string]any `yaml:"env"`
	Steps     []step         `yaml:"steps"`
	If        string         `yaml:"if"`
	Strategy  struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
}

type step struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
	Env  map[string]any `yaml:"env"`
	If   string         `yaml:"if"`
}

func parseWorkflow(path string, data []byte) (*workflow, error) {
	w := &workflow{Path: path}
	if err := yaml.Unmarshal(data, w); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return w, nil
}

func loadWorkflows(root string) ([]*workflow, error) {
	dir := filepath.Join(root, ".github", "workflows")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // no workflows is not an error; it is an absence of evidence.
	}
	var out []*workflow
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || (!strings.HasSuffix(n, ".yml") && !strings.HasSuffix(n, ".yaml")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		w, err := parseWorkflow(".github/workflows/"+n, b)
		if err != nil {
			continue // an unparseable workflow is one fewer source, not a failure.
		}
		out = append(out, w)
	}
	return out, nil
}

// triggers returns the event names a workflow declares, and for pull_request
// the paths filter attached to it.
func (w *workflow) triggers() (events []string, prPaths []string, prPathsDeclared bool) {
	n := &w.On
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}, nil, false
	case yaml.SequenceNode:
		for _, c := range n.Content {
			events = append(events, c.Value)
		}
		return events, nil, false
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			events = append(events, key)
			if key != "pull_request" && key != "pull_request_target" {
				continue
			}
			body := n.Content[i+1]
			if body.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(body.Content); j += 2 {
				if body.Content[j].Value != "paths" {
					continue
				}
				prPathsDeclared = true
				for _, p := range body.Content[j+1].Content {
					prPaths = append(prPaths, p.Value)
				}
			}
		}
		return events, prPaths, prPathsDeclared
	}
	return nil, nil, false
}

// gatesPullRequests reports whether this workflow runs on an ordinary
// source-changing pull request.
//
// This is the filter that keeps automation out of the recipe. pytorch/vision's
// tests-schedule.yml is the case that motivated it: it is a well-formed,
// fully-derivable Python workflow -- setup-python 3.10, pip install --editable
// ., pytest -- and translating it would produce a confident recipe that
// installs a torch nightly from a URL and runs a dataset *download* test. It
// is not a PR gate. Its pull_request trigger is filtered to three literal
// paths: the one test file it runs, an issue template, and itself.
//
// So: a paths filter admits source only if some pattern is a wildcard that
// could match an ordinary source file. A list of literal paths never can.
func (w *workflow) gatesPullRequests(lang string) bool {
	events, paths, declared := w.triggers()
	pr := false
	for _, e := range events {
		if e == "pull_request" || e == "pull_request_target" {
			pr = true
		}
	}
	if !pr {
		return false
	}
	if !declared || len(paths) == 0 {
		return true
	}
	exts := sourceExts(lang)
	for _, p := range paths {
		if !strings.ContainsAny(p, "*?") {
			continue // a literal file path admits only itself.
		}
		base := p[strings.LastIndexByte(p, '/')+1:]
		dot := strings.LastIndexByte(base, '.')
		if dot < 0 || strings.ContainsAny(base[dot:], "*?") {
			return true // `src/**`, `lib/*` -- a directory glob admits source.
		}
		suffix := base[dot:]
		for _, e := range exts {
			if strings.EqualFold(suffix, e) {
				return true
			}
		}
	}
	return false
}

// isReusable reports whether a workflow is callable by another one, which
// means its own triggers say nothing about whether it gates pull requests --
// its callers decide that.
func (w *workflow) isReusable() bool {
	events, _, _ := w.triggers()
	for _, e := range events {
		if e == "workflow_call" {
			return true
		}
	}
	return false
}

// callInputDefaults reads on.workflow_call.inputs.*.default so a reusable
// workflow's `${{ inputs.os }}` can be resolved even when the caller passes a
// matrix expression we cannot evaluate.
func (w *workflow) callInputDefaults() map[string]string {
	out := map[string]string{}
	n := &w.On
	if n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "workflow_call" {
			continue
		}
		call := n.Content[i+1]
		if call.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(call.Content); j += 2 {
			if call.Content[j].Value != "inputs" {
				continue
			}
			inputs := call.Content[j+1]
			for k := 0; k+1 < len(inputs.Content); k += 2 {
				name := inputs.Content[k].Value
				spec := inputs.Content[k+1]
				if spec.Kind != yaml.MappingNode {
					continue
				}
				for m := 0; m+1 < len(spec.Content); m += 2 {
					if spec.Content[m].Value == "default" {
						out["inputs."+name] = spec.Content[m+1].Value
					}
				}
			}
		}
	}
	return out
}

var exprRe = regexp.MustCompile(`\$\{\{\s*([^}]*?)\s*\}\}`)

// expand substitutes ${{ ... }} references we have resolved. The context map
// is keyed by the full reference -- "inputs.os", "matrix.python-version" -- so
// the two sources of resolved values share one lookup. Anything unresolved is
// left in place verbatim, which is what lets a caller see that a command is
// still an expression and refuse it rather than run a literal "${{ ... }}".
func expand(s string, ctx map[string]string) string {
	return exprRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSpace(exprRe.FindStringSubmatch(m)[1])
		if v, ok := ctx[inner]; ok {
			return v
		}
		return m
	})
}

func hasExpr(s string) bool { return exprRe.MatchString(s) }

// runsOnLabels flattens runs-on, which may be a scalar, a list, or a map with
// a `labels` key.
func runsOnLabels(n yaml.Node, inputs map[string]string) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{expand(n.Value, inputs)}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			out = append(out, expand(c.Value, inputs))
		}
		return out
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "labels" {
				return runsOnLabels(*n.Content[i+1], inputs)
			}
		}
	}
	return nil
}

// containerImage reads a job-level `container:`, which is the strongest signal
// a workflow can give: the repository naming the image it builds itself in.
func containerImage(n yaml.Node, inputs map[string]string) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return expand(n.Value, inputs)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "image" {
				return expand(n.Content[i+1].Value, inputs)
			}
		}
	}
	return ""
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	v, ok := m[k]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// literalInputs keeps only the caller's `with:` values that are real literals.
// `os: ${{ matrix.os }}` is dropped so the callee falls back to its declared
// default rather than inheriting an unevaluated expression.
func literalInputs(with map[string]any) map[string]string {
	out := map[string]string{}
	for _, k := range sortedKeys(with) {
		v := str(with, k)
		if v == "" || hasExpr(v) {
			continue
		}
		out["inputs."+k] = v
	}
	return out
}

// matrixContext resolves a job's strategy matrix to a single leg.
//
// This is not a nicety. `runs-on: ${{ matrix.os }}` is the overwhelmingly
// common shape -- huggingface/datasets, kornia and cli/cli all use it for
// their real test jobs -- and treating it as unresolvable disqualifies every
// one of them, leaving a lint job to win the ranking by default. The recipe
// would then be derived from a job that never compiles the project.
//
// Two rules pick the leg. For a runner axis we take the Linux value, because
// that is the platform this container actually is. For every other axis we
// take the first declared value, which is both deterministic and, in practice,
// the leg maintainers treat as primary: `test: [unit, integration]` gives
// unit, and the integration leg is the one that needs network the oracle run
// deliberately does not have. `include:` entries are additive legs and are not
// read here. The chosen leg is recorded as evidence, never assumed silently.
func matrixContext(j job) (map[string]string, []string, bool) {
	out, notes := map[string]string{}, []string(nil)
	n := j.Strategy.Matrix
	if n.Kind != yaml.MappingNode {
		return out, nil, true
	}
	linuxOK := true
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		if key == "include" || key == "exclude" || key == "fail-fast" {
			continue
		}
		if val.Kind == yaml.ScalarNode {
			out["matrix."+key] = val.Value
			continue
		}
		if val.Kind != yaml.SequenceNode || len(val.Content) == 0 {
			continue
		}
		vals := make([]string, 0, len(val.Content))
		for _, c := range val.Content {
			vals = append(vals, c.Value)
		}
		pick := vals[0]
		if isRunnerAxis(key) {
			pick = ""
			for _, v := range vals {
				if _, linux, _ := runnerImage(v); linux {
					pick = v
					break
				}
			}
			if pick == "" {
				// Every leg of this job runs on macOS or Windows.
				linuxOK = false
				continue
			}
		}
		out["matrix."+key] = pick
		if len(vals) > 1 {
			notes = append(notes, fmt.Sprintf("matrix.%s=%s of %v", key, pick, vals))
		}
	}
	return out, notes, linuxOK
}

func isRunnerAxis(key string) bool {
	switch strings.ToLower(key) {
	case "os", "runner", "platform", "runs-on", "runs_on":
		return true
	}
	return false
}
