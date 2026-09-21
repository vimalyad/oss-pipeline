package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sandboxConfigName is written inside the clone's own .git directory. That
// placement is deliberate: the file has to be bind-mountable, and anything
// under the clone is already shared with Docker, so no additional host path
// needs to be exposed to the daemon. Being inside .git also keeps it out of
// `git status` and out of any diff.
const sandboxConfigName = "config.ossp-sandbox"

// sanitiseGitConfig writes a copy of the clone's .git/config with every
// host-specific setting removed, for bind-mounting over /work/.git/config.
//
// The clone carries the pipeline's identity isolation in its local config, and
// two of those settings are host absolute paths:
//
//	credential.https://github.com.helper = <repo>/bin/gh-token-helper
//	core.hooksPath                       = <repo>/githooks
//
// Both are bind-mounted into every container along with the checkout. The
// hooks path merely breaks any git command that fires a hook. The credential
// helper is the one that matters: this package's whole premise is that a
// container receives no credential, and handing it the instructions for
// fetching one contradicts that even while the path fails to resolve inside.
// It is also a live correctness bug -- cli/cli's gitcredentials tests read it
// out of the mounted config and fail on it, which reads like a broken patch.
//
// Overriding it with GIT_CONFIG_KEY/VALUE does not work: the helper is set
// under a URL-scoped subsection, git treats helper entries as a list rather
// than a single value, and the repository-local entries survive. Replacing the
// file the container sees is what actually removes it.
func sanitiseGitConfig(clone string) (string, error) {
	gitDir := filepath.Join(clone, ".git")
	st, err := os.Stat(gitDir)
	if err != nil || !st.IsDir() {
		return "", nil // not a clone, or a worktree/submodule file: nothing to do.
	}
	src := filepath.Join(gitDir, "config")
	f, err := os.Open(src)
	if err != nil {
		return "", nil
	}
	defer f.Close()

	var b strings.Builder
	keep := true
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			keep = !strings.HasPrefix(strings.ToLower(trimmed), "[credential")
		}
		if !keep {
			continue
		}
		if k := strings.ToLower(strings.TrimSpace(keyOf(trimmed))); k == "hookspath" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("%w: read %s: %v", ErrSandbox, src, err)
	}

	dst := filepath.Join(gitDir, sandboxConfigName)
	// 0644 because the container reads it as uid 1000 and the host clone is
	// owned by someone else. It contains no secret by construction -- that is
	// the entire point of the function.
	if err := os.WriteFile(dst, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("%w: write %s: %v", ErrSandbox, dst, err)
	}
	return dst, nil
}

func keyOf(line string) string {
	i := strings.IndexByte(line, '=')
	if i < 0 {
		return ""
	}
	return line[:i]
}
