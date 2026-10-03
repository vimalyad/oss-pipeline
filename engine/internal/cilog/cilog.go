// Package cilog reads why a CI check failed, so the pipeline can fix the code
// rather than guess at it.
//
// The loop this serves is: the PR opens, a check fails, we read the log, fix
// the code, push, and watch the new checks. Not re-running the checks -- an
// outside contributor has no permission to do that, and asking returns
// "Must have admin rights to Repository". The only way to re-trigger is a new
// commit, so the fix has to be real.
//
// v1 decided what to attempt from the check's NAME, matching a regex of
// lint-ish words. A failing unit test therefore never entered the loop even
// when the log said exactly what was wrong, and an infrastructure failure was
// escalated to the user as though it were their bug.
package cilog

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Class is what kind of failure this is, which decides what happens next.
type Class string

const (
	// ClassInfra is the CI machine having a bad day: a registry returning
	// 5xx, a runner running out of disk, a cancelled job. There is no code
	// bug to fix, so it must not reach the user and must not trigger a push.
	ClassInfra Class = "infra"
	// ClassMirror is a job whose only purpose is to fail when another job
	// failed. Reporting it separately double-counts one problem.
	ClassMirror Class = "mirror"
	// ClassReal is a test, build, lint or type step that genuinely failed.
	ClassReal Class = "real"
	// ClassUnknown means the log was unreadable. Treated as real, because
	// silently ignoring a failure is worse than an unnecessary look.
	ClassUnknown Class = "unknown"
)

// Verdict explains a classification well enough to justify it in a report.
type Verdict struct {
	Class      Class
	Why        string
	FailedStep string
	// Excerpt is the part of the log worth showing a human or feeding to a
	// patch prompt. Bounded, because logs run to megabytes.
	Excerpt string
}

// infraPatterns match the log of a failure that is not about the code.
var infraPatterns = []struct {
	re  *regexp.Regexp
	why string
}{
	{regexp.MustCompile(`(?i)unexpected http response: 5\d\d|HTTP 5\d\d|502 Bad Gateway|503 Service|504 Gateway`), "upstream returned 5xx"},
	{regexp.MustCompile(`(?i)429 Too Many Requests|rate ?limit`), "rate limited"},
	{regexp.MustCompile(`(?i)no space left on device|disk quota exceeded`), "runner out of disk"},
	{regexp.MustCompile(`(?i)runner has received a shutdown signal|The runner has received`), "runner shut down"},
	{regexp.MustCompile(`(?i)failed to fetch|could not resolve host|temporary failure in name resolution|connection reset by peer|tls handshake timeout|i/o timeout`), "network failure"},
	{regexp.MustCompile(`(?i)the job was canceled|was canceled\.|##\[error\]The operation was canceled`), "job cancelled"},
	{regexp.MustCompile(`(?i)checksum mismatch|signature verification failed|corrupt(ed)? (archive|download)`), "corrupt download"},
	{regexp.MustCompile(`(?i)timed out after \d+ (minutes|seconds)|Timeout waiting for`), "job timed out"},
	{regexp.MustCompile(`(?i)docker: .*(pull access denied|manifest unknown|toomanyrequests)`), "registry failure"},
}

// setupSteps are step names whose failure is environmental almost by
// definition: nothing has been built or run yet. kornia's macOS leg failed at
// "Setup pixi" with repeated HTTP 500s and every later step was skipped.
var setupSteps = regexp.MustCompile(`(?i)^(set up job|setup |install (dependencies|pixi|uv|node|go|rust)|restore cache|checkout|download|actions/|pre-run|configure)`)

// mirrorNames are jobs that only aggregate other jobs' results. kornia's is
// literally `echo job failed && exit 1`.
var mirrorNames = regexp.MustCompile(`(?i)^(collector|collect|ci[-_ ]?ok|all[-_ ]?green|required[-_ ]?checks|ci|status|gate|conclusion|summary)$`)

// IsMirror reports whether a check only reflects another check's result.
func IsMirror(name string) bool {
	return mirrorNames.MatchString(strings.TrimSpace(name))
}

// Job is the subset of a GitHub Actions job we need.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Steps      []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Number     int    `json:"number"`
	} `json:"steps"`
}

// FailedStep is the first step that failed, or "".
func (j *Job) FailedStep() string {
	for _, s := range j.Steps {
		if s.Conclusion == "failure" {
			return s.Name
		}
	}
	return ""
}

var jobURLRe = regexp.MustCompile(`/actions/runs/(\d+)/job/(\d+)`)

// ParseDetailsURL pulls the run and job ids out of a check's details URL,
// which is the only handle the PR's check list gives us.
func ParseDetailsURL(u string) (runID, jobID int64, ok bool) {
	m := jobURLRe.FindStringSubmatch(u)
	if len(m) != 3 {
		return 0, 0, false
	}
	runID, _ = strconv.ParseInt(m[1], 10, 64)
	jobID, _ = strconv.ParseInt(m[2], 10, 64)
	return runID, jobID, true
}

// API is the narrow slice of GitHub this package needs. Declared here, at the
// consumer, so the dependency is one method wide.
type API interface {
	REST(ctx context.Context, path string, opts RESTOptions) (string, error)
}

// RESTOptions mirrors the client's options without importing it, keeping this
// package independent of the transport.
type RESTOptions struct {
	Method   string
	Paginate bool
	JQ       string
	Fields   map[string]string
}

// Fetcher reads job metadata and logs for a repo.
type Fetcher struct {
	API  API
	Repo string
	// MaxLogBytes bounds what is read into memory. Logs reach megabytes and
	// only the tail is ever useful.
	MaxLogBytes int
}

const defaultMaxLog = 256 * 1024

// Classify works out why a check failed.
//
// Step conclusions come first because they are cheap and often decisive: a
// failure in "Setup pixi" with everything after it skipped is environmental
// without reading a single log line.
func (f *Fetcher) Classify(ctx context.Context, checkName, detailsURL string) (Verdict, error) {
	if IsMirror(checkName) {
		return Verdict{Class: ClassMirror,
			Why: "this job only reports whether other jobs failed"}, nil
	}
	_, jobID, ok := ParseDetailsURL(detailsURL)
	if !ok {
		return Verdict{Class: ClassUnknown,
			Why: "no job id in the check's details URL"}, nil
	}

	var job Job
	raw, err := f.API.REST(ctx, fmt.Sprintf("repos/%s/actions/jobs/%d", f.Repo, jobID), RESTOptions{})
	if err != nil {
		return Verdict{Class: ClassUnknown, Why: "could not read the job: " + err.Error()}, err
	}
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		return Verdict{Class: ClassUnknown, Why: "job metadata was not JSON"}, err
	}

	step := job.FailedStep()
	if step != "" && setupSteps.MatchString(step) {
		// Nothing was built or run, so this cannot be about the code.
		return Verdict{Class: ClassInfra, FailedStep: step,
			Why: fmt.Sprintf("failed during %q, before anything was built", step)}, nil
	}

	logText, err := f.Logs(ctx, jobID)
	if err != nil {
		return Verdict{Class: ClassUnknown, FailedStep: step,
			Why: "could not read the log: " + err.Error()}, err
	}
	v := ClassifyLog(logText)
	v.FailedStep = step
	if v.Class == ClassReal && step != "" {
		v.Why = fmt.Sprintf("%s (step %q)", v.Why, step)
	}
	return v, nil
}

// ClassifyLog decides from log text alone. Separated so it can be tested
// against real captured logs without any network.
func ClassifyLog(text string) Verdict {
	if strings.TrimSpace(text) == "" {
		return Verdict{Class: ClassUnknown, Why: "log was empty"}
	}
	// Infrastructure wins over everything: if the runner could not fetch its
	// dependencies, whatever else the log says is downstream of that.
	for _, p := range infraPatterns {
		if m := p.re.FindString(text); m != "" {
			return Verdict{Class: ClassInfra,
				Why:     p.why + " (" + strings.TrimSpace(m) + ")",
				Excerpt: Tail(text, 40)}
		}
	}
	return Verdict{Class: ClassReal,
		Why:     "a build or test step failed",
		Excerpt: Tail(text, 120)}
}

// Logs downloads a job's log, stripped of timestamps and ANSI escapes.
//
// The result is untrusted third-party output on its way to a prompt and
// possibly a public PR body, so callers must scan it for secrets before use.
func (f *Fetcher) Logs(ctx context.Context, jobID int64) (string, error) {
	raw, err := f.API.REST(ctx,
		fmt.Sprintf("repos/%s/actions/jobs/%d/logs", f.Repo, jobID), RESTOptions{})
	if err != nil {
		return "", err
	}
	max := f.MaxLogBytes
	if max <= 0 {
		max = defaultMaxLog
	}
	if len(raw) > max {
		raw = raw[len(raw)-max:]
	}
	return Clean(raw), nil
}

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	timestampRe = regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z\s?`)
)

// Clean strips the per-line timestamps and colour codes Actions logs carry,
// which otherwise dominate a bounded excerpt.
func Clean(s string) string {
	s = timestampRe.ReplaceAllString(s, "")
	s = ansiRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// Tail returns the last n non-empty lines.
func Tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			keep = append([]string{lines[i]}, keep...)
		}
	}
	return strings.Join(keep, "\n")
}

// Summary reduces a set of failing checks to what should happen.
type Summary struct {
	Real    []string
	Infra   []string
	Mirror  []string
	Unknown []string
}

// ShouldAttemptFix reports whether there is anything worth pushing a fix for.
// Infrastructure and mirrors never justify touching the code.
func (s Summary) ShouldAttemptFix() bool { return len(s.Real) > 0 || len(s.Unknown) > 0 }

// AllTransient reports that everything failing is environmental, which means
// waiting is correct and the user should not be told.
func (s Summary) AllTransient() bool {
	return len(s.Real) == 0 && len(s.Unknown) == 0 && (len(s.Infra) > 0 || len(s.Mirror) > 0)
}
