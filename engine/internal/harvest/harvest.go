// Package harvest fetches an issue thread in full and keeps it verbatim.
//
// Maintainers bury the real requirement mid-thread: the approach they want,
// the approaches already rejected, the constraint that decides whether a patch
// is merged or closed. Missing one produces a confidently wrong pull request,
// which is worse than no pull request at all because it costs a maintainer
// their review time and reads as careless.
//
// So nothing is discarded. The complete payload is written to
// state/context/<slug>.raw.json, and the distilled brief is derived from it --
// the raw file is what a person reads when they want to check the brief's
// reading rather than trust it.
package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrHarvest = errors.New("harvest")

// API is the GraphQL surface this package needs.
type API interface {
	GraphQL(ctx context.Context, query string, vars map[string]any, v any) error
}

const threadQuery = `
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    nameWithOwner stargazerCount
    primaryLanguage { name }
    repositoryTopics(first:25) { nodes { topic { name } } }
    issue(number:$number) {
      number title url body createdAt updatedAt
      author { login } authorAssociation
      reactions { totalCount }
      labels(first:30) { nodes { name } }
      comments(first:100) {
        totalCount
        nodes {
          url createdAt body authorAssociation
          author { login }
          reactions { totalCount }
        }
      }
      timelineItems(first:100, itemTypes:[
        LABELED_EVENT, UNLABELED_EVENT, ASSIGNED_EVENT, UNASSIGNED_EVENT,
        CLOSED_EVENT, REOPENED_EVENT, CROSS_REFERENCED_EVENT,
        MARKED_AS_DUPLICATE_EVENT, LOCKED_EVENT, RENAMED_TITLE_EVENT
      ]) {
        nodes {
          __typename
          ... on LabeledEvent { createdAt label { name } actor { login } }
          ... on UnlabeledEvent { createdAt label { name } actor { login } }
          ... on AssignedEvent { createdAt actor { login } }
          ... on ClosedEvent { createdAt actor { login } }
          ... on ReopenedEvent { createdAt actor { login } }
          ... on LockedEvent { createdAt actor { login } lockReason }
          ... on MarkedAsDuplicateEvent { createdAt actor { login } }
          ... on RenamedTitleEvent { createdAt previousTitle currentTitle }
          ... on CrossReferencedEvent {
            createdAt willCloseTarget
            source {
              __typename
              ... on PullRequest { number url state merged isDraft author { login } }
              ... on Issue { number url state }
            }
          }
        }
      }
    }
  }
}`

const prThreadQuery = `
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      number url state isDraft body createdAt updatedAt
      author { login }
      additions deletions changedFiles
      commits(last:20) { nodes { commit { oid messageHeadline committedDate author { user { login } } } } }
      reviews(first:50) {
        nodes {
          state createdAt body authorAssociation
          author { login }
          comments(first:50) { nodes { path line body createdAt url } }
        }
      }
      comments(first:50) {
        nodes { createdAt body authorAssociation author { login } url }
      }
      files(first:100) { nodes { path additions deletions } }
    }
  }
}`

// Payload is everything harvested, kept as close to what GitHub returned as
// the on-disk format allows.
type Payload struct {
	Repo      RepoInfo          `json:"repo"`
	Issue     *Issue            `json:"issue"`
	LinkedPRs []json.RawMessage `json:"linked_prs"`
}

type RepoInfo struct {
	NameWithOwner string   `json:"nameWithOwner"`
	Stars         int      `json:"stars"`
	Language      string   `json:"language"`
	Topics        []string `json:"topics,omitempty"`
}

type Actor struct {
	Login string `json:"login"`
}

// Login answers "?" for a deleted account rather than dropping the line, which
// would silently remove a maintainer's statement from the thread.
func (a *Actor) LoginOr() string {
	if a == nil || a.Login == "" {
		return "?"
	}
	return a.Login
}

type Comment struct {
	URL               string `json:"url"`
	CreatedAt         string `json:"createdAt"`
	Body              string `json:"body"`
	AuthorAssociation string `json:"authorAssociation"`
	Author            *Actor `json:"author"`
	Reactions         struct {
		TotalCount int `json:"totalCount"`
	} `json:"reactions"`
}

type Issue struct {
	Number            int    `json:"number"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	Body              string `json:"body"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
	Author            *Actor `json:"author"`
	AuthorAssociation string `json:"authorAssociation"`
	Reactions         struct {
		TotalCount int `json:"totalCount"`
	} `json:"reactions"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		TotalCount int       `json:"totalCount"`
		Nodes      []Comment `json:"nodes"`
	} `json:"comments"`
	TimelineItems struct {
		Nodes []TimelineItem `json:"nodes"`
	} `json:"timelineItems"`
}

type TimelineItem struct {
	Typename  string `json:"__typename"`
	CreatedAt string `json:"createdAt"`
	Source    *struct {
		Typename string `json:"__typename"`
		Number   int    `json:"number"`
		URL      string `json:"url"`
		State    string `json:"state"`
		Merged   bool   `json:"merged"`
		IsDraft  bool   `json:"isDraft"`
		Author   *Actor `json:"author"`
	} `json:"source"`
}

// PR is the decoded view of a linked pull request. The raw JSON is kept
// alongside it so nothing GitHub sent is lost to a struct that did not know
// about a field.
type PR struct {
	Number       int    `json:"number"`
	URL          string `json:"url"`
	State        string `json:"state"`
	IsDraft      bool   `json:"isDraft"`
	Body         string `json:"body"`
	Author       *Actor `json:"author"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
	Error        string `json:"error,omitempty"`
	Reviews      struct {
		Nodes []Review `json:"nodes"`
	} `json:"reviews"`
}

type Review struct {
	State             string `json:"state"`
	CreatedAt         string `json:"createdAt"`
	Body              string `json:"body"`
	AuthorAssociation string `json:"authorAssociation"`
	Author            *Actor `json:"author"`
	Comments          struct {
		Nodes []struct {
			Path string `json:"path"`
			Line *int   `json:"line"`
			Body string `json:"body"`
		} `json:"nodes"`
	} `json:"comments"`
}

type threadResponse struct {
	Repository struct {
		NameWithOwner   string `json:"nameWithOwner"`
		StargazerCount  int    `json:"stargazerCount"`
		PrimaryLanguage *struct {
			Name string `json:"name"`
		} `json:"primaryLanguage"`
		RepositoryTopics struct {
			Nodes []struct {
				Topic struct {
					Name string `json:"name"`
				} `json:"topic"`
			} `json:"nodes"`
		} `json:"repositoryTopics"`
		Issue *Issue `json:"issue"`
	} `json:"repository"`
}

// Harvest fetches the issue thread and every pull request linked to it.
func Harvest(ctx context.Context, api API, repo string, issue int) (*Payload, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%w: %q is not owner/name", ErrHarvest, repo)
	}
	var resp threadResponse
	if err := api.GraphQL(ctx, threadQuery, map[string]any{
		"owner": owner, "name": name, "number": issue,
	}, &resp); err != nil {
		return nil, fmt.Errorf("%w: %s#%d: %v", ErrHarvest, repo, issue, err)
	}
	if resp.Repository.Issue == nil {
		return nil, fmt.Errorf("%w: %s#%d not found", ErrHarvest, repo, issue)
	}

	p := &Payload{
		Repo: RepoInfo{
			NameWithOwner: resp.Repository.NameWithOwner,
			Stars:         resp.Repository.StargazerCount,
		},
		Issue: resp.Repository.Issue,
	}
	if l := resp.Repository.PrimaryLanguage; l != nil {
		p.Repo.Language = l.Name
	}
	for _, n := range resp.Repository.RepositoryTopics.Nodes {
		p.Repo.Topics = append(p.Repo.Topics, strings.ToLower(n.Topic.Name))
	}

	// Linked pull requests matter twice over: a stale one may be worth taking
	// over, and its review comments are often where the maintainer actually
	// stated the specification.
	seen := map[int]bool{}
	for _, item := range p.Issue.TimelineItems.Nodes {
		src := item.Source
		if src == nil || src.Typename != "PullRequest" || seen[src.Number] {
			continue
		}
		seen[src.Number] = true

		var prResp struct {
			Repository struct {
				PullRequest json.RawMessage `json:"pullRequest"`
			} `json:"repository"`
		}
		err := api.GraphQL(ctx, prThreadQuery, map[string]any{
			"owner": owner, "name": name, "number": src.Number,
		}, &prResp)
		if err != nil || len(prResp.Repository.PullRequest) == 0 {
			// Recorded, not dropped. A pull request we could not read is a
			// gap a person may need to look at, and a silent omission looks
			// exactly like an issue with no linked work.
			stub, _ := json.Marshal(map[string]any{
				"number": src.Number, "url": src.URL,
				"error": errText(err),
			})
			p.LinkedPRs = append(p.LinkedPRs, stub)
			continue
		}
		p.LinkedPRs = append(p.LinkedPRs, prResp.Repository.PullRequest)
	}
	return p, nil
}

func errText(err error) string {
	if err == nil {
		return "pull request returned empty"
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Save writes the payload where a person can read it.
func Save(root, slug string, p *Payload) (string, error) {
	dir := filepath.Join(root, "state", "context")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, slug+".raw.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// Load reads a previously harvested payload.
func Load(root, slug string) (*Payload, error) {
	b, err := os.ReadFile(filepath.Join(root, "state", "context", slug+".raw.json"))
	if err != nil {
		return nil, err
	}
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrHarvest, slug, err)
	}
	return &p, nil
}

// DefaultMaxChars bounds the rendered transcript.
const DefaultMaxChars = 60000

// Render flattens the payload into the transcript a brief is extracted from.
//
// The author association is kept on every single line. An OWNER or MEMBER
// statement is the specification; a drive-by NONE comment is one stranger's
// opinion. Collapsing that distinction is how a pipeline ends up implementing
// a suggestion nobody with authority ever endorsed.
func Render(p *Payload, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	if p == nil || p.Issue == nil {
		return ""
	}
	iss := p.Issue
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	var labels []string
	for _, l := range iss.Labels.Nodes {
		labels = append(labels, l.Name)
	}
	line("REPO: %s (%s, %d stars)", p.Repo.NameWithOwner, p.Repo.Language, p.Repo.Stars)
	line("ISSUE #%d: %s", iss.Number, iss.Title)
	line("URL: %s", iss.URL)
	line("LABELS: %s", strings.Join(labels, ", "))
	line("")
	line("--- opened by %s [%s] at %s ---", iss.Author.LoginOr(), iss.AuthorAssociation, iss.CreatedAt)
	line("%s", strings.TrimSpace(iss.Body))
	line("")

	for _, c := range iss.Comments.Nodes {
		line("--- comment by %s [%s] at %s (%d reactions) %s ---",
			c.Author.LoginOr(), c.AuthorAssociation, c.CreatedAt, c.Reactions.TotalCount, c.URL)
		line("%s", strings.TrimSpace(c.Body))
		line("")
	}

	for _, raw := range p.LinkedPRs {
		var pr PR
		if json.Unmarshal(raw, &pr) != nil || pr.Error != "" {
			if pr.Number != 0 {
				line("=== LINKED PR #%d: could not be read (%s) ===", pr.Number, pr.Error)
				line("")
			}
			continue
		}
		draft := ""
		if pr.IsDraft {
			draft = ", draft"
		}
		line("=== LINKED PR #%d (%s%s) by %s +%d/-%d across %d files ===",
			pr.Number, pr.State, draft, pr.Author.LoginOr(),
			pr.Additions, pr.Deletions, pr.ChangedFiles)
		line("%s", truncate(strings.TrimSpace(pr.Body), 2000))
		line("")
		for _, rv := range pr.Reviews.Nodes {
			line("--- PR review [%s] by %s [%s] at %s ---",
				rv.State, rv.Author.LoginOr(), rv.AuthorAssociation, rv.CreatedAt)
			if s := strings.TrimSpace(rv.Body); s != "" {
				line("%s", s)
			}
			for _, rc := range rv.Comments.Nodes {
				where := rc.Path
				if rc.Line != nil {
					where = fmt.Sprintf("%s:%d", rc.Path, *rc.Line)
				}
				line("  [%s] %s", where, strings.TrimSpace(rc.Body))
			}
			line("")
		}
	}

	text := b.String()
	// Keep both ends. The opening states the problem and the last few comments
	// are where a maintainer usually settles the approach; dropping the tail
	// to fit would throw away the decision and keep the debate.
	head, tail := headAndTail(text, maxChars)
	if tail == "" {
		return text
	}
	return head + "\n\n[... middle of thread truncated ...]\n\n" + tail
}

// truncate cuts to n runes, not n bytes.
//
// Slicing a Go string by byte offset can land in the middle of a multi-byte
// character, which puts invalid UTF-8 into a prompt or, worse, into a comment
// posted under the user's name. It also makes the cut arrive early for any
// text that is not pure ASCII: an em dash spends three of the budget instead
// of one, and issue threads are full of them. Comparing a rendered thread
// against the Python implementation is what surfaced this -- the two
// transcripts diverged by 40 bytes on kornia's thread and nothing else.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s // a string of n bytes can never exceed n runes.
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// headAndTail keeps both ends of an over-long transcript, on rune boundaries.
func headAndTail(s string, n int) (head, tail string) {
	r := []rune(s)
	if len(r) <= n {
		return s, ""
	}
	half := n / 2
	return string(r[:half]), string(r[len(r)-half:])
}

// LinkedPRNumbers returns the pull requests this issue references, for
// internal/contest to classify.
func (p *Payload) LinkedPRNumbers() []int {
	var out []int
	seen := map[int]bool{}
	for _, raw := range p.LinkedPRs {
		var pr struct {
			Number int `json:"number"`
		}
		if json.Unmarshal(raw, &pr) == nil && pr.Number != 0 && !seen[pr.Number] {
			seen[pr.Number] = true
			out = append(out, pr.Number)
		}
	}
	return out
}
