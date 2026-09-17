package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func stub(out string, err error) *Client {
	c := New(nil)
	c.exec = func(ctx context.Context, args []string, stdin string, env []string) (string, string, error) {
		return out, "", err
	}
	return c
}

// TestJudgeDisablesEveryTool is a security test, not a behaviour test. Judge
// is routinely handed a third-party issue thread; anything in that text which
// reads like an instruction must not be able to reach a file or a shell.
func TestJudgeDisablesEveryTool(t *testing.T) {
	var got []string
	c := New(nil)
	c.exec = func(ctx context.Context, args []string, stdin string, env []string) (string, string, error) {
		got = args
		return "ok", "", nil
	}
	if _, err := c.Judge(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--disallowed-tools") {
		t.Fatal("Judge must disable tools")
	}
	for _, tool := range []string{"Read", "Write", "Edit", "Bash"} {
		if !strings.Contains(joined, tool) {
			t.Errorf("%s is not disabled: %v", tool, got)
		}
	}
	if strings.Contains(joined, "acceptEdits") || strings.Contains(joined, "bypassPermissions") {
		t.Error("Judge must never run with edit permissions")
	}
}

func TestExtractJSON(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		wantErr        bool
	}{
		{"bare object", `{"a":1}`, `{"a":1}`, false},
		{"with prose", "Here you go:\n{\"a\":1}\nHope that helps", `{"a":1}`, false},
		{"fenced", "```json\n{\"a\":1}\n```", `{"a":1}`, false},
		{"array", `[{"a":1},{"b":2}]`, `[{"a":1},{"b":2}]`, false},
		{"nested", `{"a":{"b":[1,2]}}`, `{"a":{"b":[1,2]}}`, false},
		// A brace inside a string must not close the object -- issue threads
		// are full of code samples containing braces.
		{"brace in string", `{"note":"use } carefully"}`, `{"note":"use } carefully"}`, false},
		{"escaped quote", `{"note":"say \"hi\" {"}`, `{"note":"say \"hi\" {"}`, false},
		{"no json", "I could not determine that", "", true},
		{"unterminated", `{"a":1`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractJSON(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestJudgeJSONDecodes(t *testing.T) {
	var v struct {
		Class string `json:"class"`
	}
	err := stub("```json\n{\"class\":\"mechanical\"}\n```", nil).
		JudgeJSON(context.Background(), "p", &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.Class != "mechanical" {
		t.Fatalf("class = %q", v.Class)
	}
}

// An unusable answer gets one more try with an explicit complaint, because a
// failed judgement blocks a whole stage.
func TestJudgeJSONRetriesOnce(t *testing.T) {
	calls := 0
	c := New(nil)
	c.exec = func(ctx context.Context, args []string, stdin string, env []string) (string, string, error) {
		calls++
		if calls == 1 {
			return "I'm not sure what you mean.", "", nil
		}
		return `{"class":"informational"}`, "", nil
	}
	var v struct {
		Class string `json:"class"`
	}
	if err := c.JudgeJSON(context.Background(), "p", &v); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if v.Class != "informational" {
		t.Fatalf("class = %q", v.Class)
	}
}

func TestJudgeJSONGivesUpAfterTwo(t *testing.T) {
	var v map[string]any
	err := stub("still no json", nil).JudgeJSON(context.Background(), "p", &v)
	if !errors.Is(err, ErrLLM) {
		t.Fatalf("want ErrLLM, got %v", err)
	}
}

func TestRender(t *testing.T) {
	got := Render("Fix {{repo}}#{{issue}}", map[string]string{
		"repo": "a/b", "issue": "7",
	})
	if got != "Fix a/b#7" {
		t.Fatalf("got %q", got)
	}
}
