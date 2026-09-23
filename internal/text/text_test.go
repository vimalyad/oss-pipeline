package text

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClipNeverProducesInvalidUTF8 is the whole reason this package exists. A
// byte slice can land inside a multi-byte character, and the result goes into
// prompts and into comments posted under the user's name.
func TestClipNeverProducesInvalidUTF8(t *testing.T) {
	// Em dashes and arrows are three bytes each; these are the characters
	// real issue threads are full of.
	s := strings.Repeat("a—b→c", 50)
	for n := 0; n < 200; n++ {
		got := Clip(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("Clip(s, %d) produced invalid UTF-8: %q", n, got)
		}
		if utf8.RuneCountInString(got) > n {
			t.Fatalf("Clip(s, %d) returned %d runes", n, utf8.RuneCountInString(got))
		}
	}
}

// TestClipCountsCharactersNotBytes: byte counting makes the cut arrive early
// for any non-ASCII text, which is how a 2000-character budget silently
// became about 700 characters of a thread full of dashes.
func TestClipCountsCharactersNotBytes(t *testing.T) {
	s := strings.Repeat("—", 100) // 100 runes, 300 bytes
	if got := utf8.RuneCountInString(Clip(s, 100)); got != 100 {
		t.Errorf("clipped to %d runes, want all 100", got)
	}
	if got := Clip(s, 150); got != s {
		t.Errorf("a budget larger than the string still trimmed it")
	}
}

func TestClipEdges(t *testing.T) {
	if got := Clip("abc", 0); got != "" {
		t.Errorf("Clip(_, 0) = %q", got)
	}
	if got := Clip("abc", -1); got != "" {
		t.Errorf("Clip(_, -1) = %q", got)
	}
	if got := Clip("", 5); got != "" {
		t.Errorf("Clip(\"\", 5) = %q", got)
	}
	if got := Clip("abc", 3); got != "abc" {
		t.Errorf("exact fit = %q", got)
	}
}

func TestEllipsisMarksOnlyWhenItCut(t *testing.T) {
	if got := Ellipsis("short", 20); got != "short" {
		t.Errorf("= %q; an untrimmed string must not gain a marker", got)
	}
	got := Ellipsis("a much longer string than the budget", 10)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("= %q; a silent truncation reads as a complete message", got)
	}
	if utf8.RuneCountInString(strings.TrimSuffix(got, "...")) != 10 {
		t.Errorf("= %q", got)
	}
}

func TestHeadTail(t *testing.T) {
	if h, tl := HeadTail("short", 100); h != "short" || tl != "" {
		t.Errorf("= %q, %q; an untrimmed string has no tail", h, tl)
	}
	s := "START" + strings.Repeat("—", 500) + "END"
	h, tl := HeadTail(s, 40)
	if !strings.HasPrefix(h, "START") {
		t.Errorf("head = %q; the opening states the problem", h)
	}
	if !strings.HasSuffix(tl, "END") {
		t.Errorf("tail = %q; the end is where the decision is", tl)
	}
	if !utf8.ValidString(h) || !utf8.ValidString(tl) {
		t.Error("split inside a character")
	}
	if h, tl := HeadTail("anything", 0); h != "" || tl != "" {
		t.Errorf("= %q, %q", h, tl)
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("one\ntwo\nthree", 100); got != "one ..." {
		t.Errorf("= %q", got)
	}
	if got := FirstLine("  single  ", 100); got != "single" {
		t.Errorf("= %q", got)
	}
	if got := FirstLine(strings.Repeat("x", 50), 10); got != strings.Repeat("x", 10)+"..." {
		t.Errorf("= %q", got)
	}
}
