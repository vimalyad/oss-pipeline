// Package watch tracks an open pull request through CI, bots and human review.
//
// The autonomy split is decided here and nowhere else: a failure the code is
// actually responsible for goes into the fix-and-push loop without asking;
// anything needing a human reply or a change of approach is drafted and
// queued. Public comments carry the user's name, so nothing posts one
// unreviewed.
package watch

import (
	"context"
	"fmt"
	"strings"

	"github.com/vimalyad/osspipeline/internal/cilog"
	"github.com/vimalyad/osspipeline/internal/model"
)

// API is the GraphQL surface this package needs. Declared here so watch never
// depends on the whole client.
type API interface {
	GraphQL(ctx context.Context, query string, vars map[string]any, v any) error
}

// CheckClassifier decides whether a failing check is the code's fault, by
// reading the job's log rather than its name.
type CheckClassifier interface {
	Classify(ctx context.Context, checkName, detailsURL string) (cilog.Verdict, error)
}

// Class is the action a piece of feedback requires.
type Class string

const (
	// ClassMechanical is fixed and pushed without asking.
	ClassMechanical Class = "mechanical"
	// ClassNeedsReply is a question or objection for a human to answer.
	ClassNeedsReply Class = "needs_reply"
	// ClassNeedsDesign means the request changes the approach or scope.
	ClassNeedsDesign Class = "needs_design"
	// ClassInformational is a bot summary, a passing notice or praise.
	ClassInformational Class = "informational"
)

// Item is one piece of feedback: a review, an inline comment, an issue
// comment, or a failing check.
type Item struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Kind   string `json:"kind"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	Class  Class  `json:"cls"`
	Why    string `json:"why"`
	Action string `json:"action"`
}

// Check is one failing or held status context.
type Check struct {
	Name string
	URL  string
}

// State is one poll's view of a pull request.
type State struct {
	Number    int
	URL       string
	State     string // OPEN, CLOSED, MERGED
	Merged    bool
	IsDraft   bool
	Mergeable string
	UpdatedAt string
	HeadSHA   string

	Failing []Check
	// Awaiting are checks held at ACTION_REQUIRED. GitHub holds workflow runs
	// from first-time contributors until a maintainer approves them: not a
	// failure, and not our move. Pushing at it again does not help.
	Awaiting []Check

	Items []Item
}

// Buckets groups items by the action they require.
func (s State) Buckets() map[Class][]Item {
	out := map[Class][]Item{}
	for _, it := range s.Items {
		out[it.Class] = append(out[it.Class], it)
	}
	return out
}

// Queued are the items a human has to deal with.
func (s State) Queued() []Item {
	b := s.Buckets()
	return append(append([]Item{}, b[ClassNeedsReply]...), b[ClassNeedsDesign]...)
}

const prStateQuery = `
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      number url state merged isDraft mergeable updatedAt
      commits(last:1) {
        nodes { commit {
          oid
          statusCheckRollup {
            state
            contexts(first:60) {
              nodes {
                __typename
                ... on CheckRun { name conclusion detailsUrl }
                ... on StatusContext { context state targetUrl }
              }
            }
          }
        } }
      }
      reviews(last:20) {
        nodes { id state createdAt body author { login } authorAssociation
                comments(first:40) { nodes { id path line body url } } }
      }
      comments(last:30) {
        nodes { id createdAt body url author { login } authorAssociation }
      }
    }
  }
}`

type prResponse struct {
	Repository struct {
		PullRequest struct {
			Number    int    `json:"number"`
			URL       string `json:"url"`
			State     string `json:"state"`
			Merged    bool   `json:"merged"`
			IsDraft   bool   `json:"isDraft"`
			Mergeable string `json:"mergeable"`
			UpdatedAt string `json:"updatedAt"`
			Commits   struct {
				Nodes []struct {
					Commit struct {
						OID               string `json:"oid"`
						StatusCheckRollup *struct {
							State    string `json:"state"`
							Contexts struct {
								Nodes []struct {
									Typename   string `json:"__typename"`
									Name       string `json:"name"`
									Conclusion string `json:"conclusion"`
									DetailsURL string `json:"detailsUrl"`
									Context    string `json:"context"`
									State      string `json:"state"`
									TargetURL  string `json:"targetUrl"`
								} `json:"nodes"`
							} `json:"contexts"`
						} `json:"statusCheckRollup"`
					} `json:"commit"`
				} `json:"nodes"`
			} `json:"commits"`
			Reviews struct {
				Nodes []struct {
					ID       string  `json:"id"`
					State    string  `json:"state"`
					Body     string  `json:"body"`
					Author   *author `json:"author"`
					Comments struct {
						Nodes []struct {
							ID   string `json:"id"`
							Path string `json:"path"`
							Line *int   `json:"line"`
							Body string `json:"body"`
							URL  string `json:"url"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"nodes"`
			} `json:"reviews"`
			Comments struct {
				Nodes []struct {
					ID     string  `json:"id"`
					Body   string  `json:"body"`
					URL    string  `json:"url"`
					Author *author `json:"author"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

type author struct {
	Login string `json:"login"`
}

func (a *author) login() string {
	if a == nil || a.Login == "" {
		// A deleted account comes back as null. Losing the item because of
		// that would be worse than not knowing who wrote it.
		return "?"
	}
	return a.Login
}

// Poll fetches the pull request and returns its state with every piece of
// feedback not already recorded in WatchSeen. Classification is a separate
// step so it can be tested, and skipped, without the network.
func Poll(ctx context.Context, api API, c *model.Candidate) (State, error) {
	if c.PRNumber == nil {
		return State{}, fmt.Errorf("watch %s: no PR number", c.Slug())
	}
	owner, name, ok := strings.Cut(c.Repo, "/")
	if !ok {
		return State{}, fmt.Errorf("watch: %q is not owner/name", c.Repo)
	}

	var resp prResponse
	err := api.GraphQL(ctx, prStateQuery, map[string]any{
		"owner": owner, "name": name, "number": *c.PRNumber,
	}, &resp)
	if err != nil {
		return State{}, fmt.Errorf("watch %s: %w", c.Slug(), err)
	}
	pr := resp.Repository.PullRequest

	st := State{
		Number: pr.Number, URL: pr.URL, State: pr.State, Merged: pr.Merged,
		IsDraft: pr.IsDraft, Mergeable: pr.Mergeable, UpdatedAt: pr.UpdatedAt,
	}
	if len(pr.Commits.Nodes) > 0 {
		commit := pr.Commits.Nodes[0].Commit
		st.HeadSHA = commit.OID
		if commit.StatusCheckRollup != nil {
			for _, ctxNode := range commit.StatusCheckRollup.Contexts.Nodes {
				switch ctxNode.Typename {
				case "CheckRun":
					switch ctxNode.Conclusion {
					case "FAILURE", "TIMED_OUT", "CANCELLED":
						st.Failing = append(st.Failing, Check{ctxNode.Name, ctxNode.DetailsURL})
					case "ACTION_REQUIRED":
						st.Awaiting = append(st.Awaiting, Check{ctxNode.Name, ctxNode.DetailsURL})
					}
				case "StatusContext":
					if ctxNode.State == "FAILURE" || ctxNode.State == "ERROR" {
						st.Failing = append(st.Failing, Check{ctxNode.Context, ctxNode.TargetURL})
					}
				}
			}
		}
	}

	seen := seenSet(c)
	for _, rv := range pr.Reviews.Nodes {
		who := rv.Author.login()
		if !seen[rv.ID] && strings.TrimSpace(rv.Body) != "" {
			st.Items = append(st.Items, Item{
				ID: rv.ID, Author: who, Kind: "review " + rv.State, Body: rv.Body,
			})
		}
		for _, rc := range rv.Comments.Nodes {
			if seen[rc.ID] {
				continue
			}
			st.Items = append(st.Items, Item{
				ID: rc.ID, Author: who, Kind: inlineKind(rc.Path, rc.Line),
				Body: rc.Body, URL: rc.URL,
			})
		}
	}
	for _, cm := range pr.Comments.Nodes {
		if seen[cm.ID] {
			continue
		}
		st.Items = append(st.Items, Item{
			ID: cm.ID, Author: cm.Author.login(), Kind: "comment", Body: cm.Body, URL: cm.URL,
		})
	}
	return st, nil
}

func inlineKind(path string, line *int) string {
	if line == nil {
		return "inline " + path
	}
	return fmt.Sprintf("inline %s:%d", path, *line)
}

func seenSet(c *model.Candidate) map[string]bool {
	s := make(map[string]bool, len(c.WatchSeen))
	for _, id := range c.WatchSeen {
		s[id] = true
	}
	return s
}

// CheckKey identifies a failing check on one commit, so the same red check on
// the same head is not reported twice.
func CheckKey(headSHA, name string) string {
	return "check:" + headSHA + ":" + name
}
