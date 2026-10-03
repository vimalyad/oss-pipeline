// Package text holds the string trimming every other package needs, in one
// place because the correctness is shared and subtle.
//
// Go slices strings by byte. Every `s[:n]` in this codebase was therefore
// wrong twice over: it cuts early for anything that is not pure ASCII -- an em
// dash spends three of the budget instead of one, and issue threads are full
// of them -- and the cut can land in the middle of a character, putting
// invalid UTF-8 into an LLM prompt or into a comment posted under the user's
// name.
//
// It surfaced by comparing a rendered issue thread against the Python
// implementation, which slices by character: the two transcripts differed by
// 40 bytes on one thread and agreed everywhere else.
package text

import "strings"

// Clip cuts s to at most n runes.
func Clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	// A string of n bytes or fewer cannot hold more than n runes, so the
	// common case costs one comparison and no allocation.
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Ellipsis cuts s to n runes and marks that it did.
//
// The marker matters wherever the result reaches a person or a model: a
// silently truncated error message reads as a complete one, and a model shown
// a cut-off diff will reason about the part it was given as though that were
// the whole change.
func Ellipsis(s string, n int) string {
	out := Clip(s, n)
	if out == s {
		return s
	}
	return out + "..."
}

// HeadTail keeps both ends of an over-long string, returning an empty tail
// when no trimming was needed.
//
// Used where the beginning and the end both carry meaning and the middle
// carries less: an issue thread opens with the problem and closes with the
// maintainer's decision, so dropping the tail to fit would throw away the
// decision and keep the debate.
func HeadTail(s string, n int) (head, tail string) {
	if n <= 0 {
		return "", ""
	}
	if len(s) <= n {
		return s, ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s, ""
	}
	half := n / 2
	return string(r[:half]), string(r[len(r)-half:])
}

// FirstLine reduces s to its first line, clipped, for a one-line report entry.
func FirstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " ..."
	}
	return Ellipsis(strings.TrimSpace(s), n)
}
