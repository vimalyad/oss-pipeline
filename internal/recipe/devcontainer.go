package recipe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type devcontainer struct {
	Image string `json:"image"`
	Build *struct {
		Dockerfile string `json:"dockerfile"`
		Context    string `json:"context"`
	} `json:"build"`
	RemoteUser   string            `json:"remoteUser"`
	Features     map[string]any    `json:"features"`
	PostCreate   json.RawMessage   `json:"postCreateCommand"`
	ContainerEnv map[string]string `json:"containerEnv"`
}

var devcontainerPaths = []string{
	".devcontainer/devcontainer.json",
	".devcontainer.json",
	".devcontainer/devcontainer.jsonc",
}

// fromDevcontainer reads a repository's declared development image. This tier
// sits above CI because a devcontainer is a statement of intent -- someone
// chose this image to develop the project in -- whereas a workflow is
// something we have to infer from.
func fromDevcontainer(root, repo string) (Recipe, bool, error) {
	for _, rel := range devcontainerPaths {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		var dc devcontainer
		if err := json.Unmarshal(stripJSONC(b), &dc); err != nil {
			return Recipe{}, false, fmt.Errorf("parse %s: %w", rel, err)
		}
		r := Recipe{
			Repo: repo, Source: SourceDevcontainer,
			Evidence: []string{rel}, Platform: "linux/arm64",
		}
		switch {
		case dc.Image != "":
			r.BaseImage = dc.Image
		case dc.Build != nil && dc.Build.Dockerfile != "":
			ctx := dc.Build.Context
			if ctx == "" {
				ctx = filepath.Dir(rel)
			}
			r.Evidence = append(r.Evidence, filepath.Join(ctx, dc.Build.Dockerfile))
			b2, err := os.ReadFile(filepath.Join(root, ctx, dc.Build.Dockerfile))
			if err != nil {
				continue
			}
			r.BaseImage = firstFrom(string(b2))
		}
		if r.BaseImage == "" {
			continue
		}
		// Features are installed by the devcontainer CLI, which we do not
		// run. Saying so is better than silently producing an image that is
		// missing tools the repository expects.
		for _, f := range sortedKeys(dc.Features) {
			r.Unresolved = append(r.Unresolved,
				fmt.Sprintf("%s: devcontainer feature %q not installed", rel, f))
		}
		if len(dc.PostCreate) > 0 {
			r.Unresolved = append(r.Unresolved,
				fmt.Sprintf("%s: postCreateCommand not run", rel))
		}
		// The image says nothing about how the project is tested, so those
		// commands still come from toolchain detection.
		return r, true, nil
	}
	return Recipe{}, false, nil
}

// stripJSONC removes // and /* */ comments and trailing commas.
//
// devcontainer.json is JSON with comments by specification, and encoding/json
// rejects both. The string-awareness is not optional: an image ref or a
// documentation URL contains "//", and a naive strip turns
// "https://example.com" into "https:" and then fails to parse for a reason
// that points at the wrong line.
func stripJSONC(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return dropTrailingCommas(out)
}

func dropTrailingCommas(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// Complete reports whether a recipe can actually verify a patch. A recipe with
// no test command builds a container that proves nothing.
func (r Recipe) Complete() bool { return r.BaseImage != "" && len(r.Test) > 0 }

func (r Recipe) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s via %s: %s", r.Repo, r.Source, r.BaseImage)
	if len(r.Evidence) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(r.Evidence, ", "))
	}
	return b.String()
}
