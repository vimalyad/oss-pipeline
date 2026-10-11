package identity

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The daily runner links its credentials into a clean checkout. Exercise the
// real shell helper against that layout, without reading any real credentials.
func TestCredentialHelperTokenPermissions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux token-file fallback")
	}
	helper, err := os.ReadFile("../../../bin/gh-token-helper")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mode os.FileMode
		link bool
		ok   bool
	}{
		{"private file", 0o600, false, true},
		{"private linked file", 0o600, true, true},
		{"read-only linked file", 0o400, true, true},
		{"readable linked file", 0o644, true, false},
		{"readable file", 0o644, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Dir(filepath.Dir(writeEnv(t, sample)))
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(bin, "gh-token-helper")
			if err := os.WriteFile(path, helper, 0o755); err != nil {
				t.Fatal(err)
			}
			// An isolated PATH also ensures a host's macOS security shim cannot
			// bypass the Linux file check under test.
			for _, tool := range []string{"dirname", "stat", "tr"} {
				p, err := exec.LookPath(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			tokenPath := filepath.Join(root, "config", "gh-token")
			target := tokenPath
			if tc.link {
				target = filepath.Join(root, "private-token")
				if err := os.Symlink(target, tokenPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte("fake-token\n"), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, tc.mode); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", path, "get")
			cmd.Env = []string{"PATH=" + bin}
			cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
			out, err := cmd.CombinedOutput()
			if tc.ok {
				if err != nil || string(out) != "username=someone\npassword=fake-token\n" {
					t.Fatalf("helper: %v, %s", err, out)
				}
			} else if err == nil || strings.Contains(string(out), "fake-token") {
				t.Fatalf("insecure file was not refused: %v, %s", err, out)
			}
		})
	}
}
