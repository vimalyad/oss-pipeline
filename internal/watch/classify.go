package watch

import (
	"context"

	"github.com/vimalyad/osspipeline/internal/cilog"
)

// ClassifyChecks turns failing checks into items, reading each job's log
// rather than matching its name.
//
// This is the correction to v1, which decided from the check name alone: a
// name matching /lint|format|fmt|.../ was mechanical and everything else was a
// design question for a human. A failing unit test therefore never entered the
// fix loop, which is the one failure most worth fixing automatically -- the
// log names the assertion, the file and the line.
//
// Reading the log also separates the two cases a name can never distinguish. A
// registry returning 5xx and a genuinely failing test both surface as a red
// check called "tests"; only one of them is about the diff.
func ClassifyChecks(ctx context.Context, cl CheckClassifier, st *State, seen map[string]bool) {
	for _, chk := range st.Failing {
		key := CheckKey(st.HeadSHA, chk.Name)
		if seen[key] {
			continue
		}
		it := Item{ID: key, Author: "CI", Kind: "check", URL: chk.URL}

		v, err := cl.Classify(ctx, chk.Name, chk.URL)
		switch {
		case err != nil, v.Class == cilog.ClassUnknown:
			// The log could not be read, so the failure cannot be reproduced
			// and a fix would be a guess. A human look is cheap; pushing a
			// guess to someone else's CI is not.
			it.Class, it.Why = ClassNeedsReply, unreadable(v, err)
			it.Body = "check '" + chk.Name + "' failed and its log could not be read"
			it.Action = "read " + chk.URL + " and decide"
		case v.Class == cilog.ClassMirror:
			// A mirror job only reports that some other job failed. Counting
			// it separately turns one problem into two for a human to read.
			it.Class, it.Why = ClassInformational, v.Why
			it.Body = "check '" + chk.Name + "' failed (mirrors another job)"
		case v.Class == cilog.ClassInfra:
			// The CI machine having a bad day. Escalating it asks the user to
			// debug someone else's registry outage.
			it.Class, it.Why = ClassInformational, v.Why
			it.Body = "check '" + chk.Name + "' failed: " + v.Why
		default:
			it.Class, it.Why = ClassMechanical, v.Why
			it.Body = "check '" + chk.Name + "' failed: " + v.Why
			if v.Excerpt != "" {
				it.Body += "\n\n" + v.Excerpt
			}
			it.Action = "make the '" + chk.Name + "' check pass"
		}
		st.Items = append(st.Items, it)
	}
}

func unreadable(v cilog.Verdict, err error) string {
	if v.Why != "" {
		return v.Why
	}
	if err != nil {
		return err.Error()
	}
	return "the job log was unreadable"
}

// FeedbackClassifier splits human and bot feedback by the action it requires.
// The implementation is an LLM call; the interface is here so a watch cycle
// can be tested without one.
type FeedbackClassifier interface {
	Classify(ctx context.Context, items []Item) (map[string]Verdict, error)
}

// Verdict is one classification of one item.
type Verdict struct {
	Class  Class  `json:"class"`
	Why    string `json:"why"`
	Action string `json:"action"`
}

// ClassifyFeedback labels every non-check item.
//
// Anything the classifier does not answer for, or answers with a class we do
// not recognise, becomes needs_reply. Defaulting towards a human is the only
// safe direction: the alternative defaults are to silently drop a maintainer's
// question, or to let an unrecognised label authorise an automatic push.
func ClassifyFeedback(ctx context.Context, fc FeedbackClassifier, st *State) error {
	var pending []Item
	for _, it := range st.Items {
		if it.Class == "" {
			pending = append(pending, it)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	verdicts, err := fc.Classify(ctx, pending)
	if err != nil {
		// Leave them unclassified; the loop below turns that into needs_reply
		// so a failed classification never loses a comment.
		verdicts = nil
	}
	for i := range st.Items {
		if st.Items[i].Class != "" {
			continue
		}
		v, ok := verdicts[st.Items[i].ID]
		if !ok || !v.Class.valid() {
			st.Items[i].Class = ClassNeedsReply
			if st.Items[i].Why == "" {
				st.Items[i].Why = "unclassified"
			}
			continue
		}
		st.Items[i].Class, st.Items[i].Why, st.Items[i].Action = v.Class, v.Why, v.Action
	}
	return err
}

func (c Class) valid() bool {
	switch c {
	case ClassMechanical, ClassNeedsReply, ClassNeedsDesign, ClassInformational:
		return true
	}
	return false
}
