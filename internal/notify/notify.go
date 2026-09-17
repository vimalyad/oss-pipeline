// Package notify reaches the user on their phone.
//
// This exists because the v1 loop detected everything and closed nothing: five
// replies sat drafted and unsent for days while a reviewer waited, and nothing
// said so. The pipeline knew; the user did not.
//
// ntfy rather than Telegram, for two reasons. A Telegram bot needs a token to
// store and rotate, and a Telegram account is trivially linkable to a person --
// which cuts against the whole reason this pipeline keeps identities apart.
// An ntfy topic is a capability: knowing it is the whole authorisation. So the
// topic is a random token in a gitignored file, and the rule inherited from
// the status gist applies -- nothing goes into a notification that is not
// already public on GitHub.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrNotify = errors.New("notify")

// Priority follows ntfy's scale. The taxonomy is deliberate: a push is for
// something that has stopped and needs the user, or an outcome that is final.
// Everything else waits for the daily digest, or the phone becomes noise and
// stops being read at all.
type Priority int

const (
	PriorityMin     Priority = 1
	PriorityLow     Priority = 2
	PriorityDefault Priority = 3
	PriorityHigh    Priority = 4
	PriorityUrgent  Priority = 5
)

// Action is a button on the notification. The user decides from a phone, so
// every decision has to be reachable without a keyboard.
type Action struct {
	Label string
	Verb  string // approve | reject | reply-post | halt | resume | view
	Arg   string
	URL   string // for "view": opened directly, no round trip
}

// Event is one thing worth telling the user about.
type Event struct {
	Kind     string
	Slug     string
	Title    string
	Body     string
	URL      string // tapping the notification opens this
	Priority Priority
	Tags     []string
	Actions  []Action
	// DedupeKey suppresses repeats. The watch runs hourly; without this a
	// single failing check would notify roughly a hundred times.
	DedupeKey string
}

// Config is what the sender needs. Topic is read from a file rather than
// inlined so it can be gitignored.
type Config struct {
	Server       string
	Topic        string
	CommandTopic string
	QuietStart   string // "23:00"
	QuietEnd     string // "08:00"
	Location     *time.Location
}

// LoadConfig reads the topics from their files. A missing topic is not an
// error: it means notifications are not set up yet, and the pipeline must
// still run.
func LoadConfig(root, server, topicFile, cmdFile, start, end, tz string) (Config, error) {
	c := Config{Server: strings.TrimRight(server, "/"),
		QuietStart: start, QuietEnd: end, Location: time.UTC}
	if c.Server == "" {
		c.Server = "https://ntfy.sh"
	}
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			c.Location = loc
		}
	}
	c.Topic = readTopic(root, topicFile)
	c.CommandTopic = readTopic(root, cmdFile)
	return c, nil
}

func readTopic(root, rel string) string {
	if rel == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c Config) Configured() bool { return c.Topic != "" }

// Quiet reports whether now falls inside quiet hours. Priority >= High
// ignores it: a merged PR or a wedged pipeline is worth waking up for.
func (c Config) Quiet(now time.Time) bool {
	if c.QuietStart == "" || c.QuietEnd == "" {
		return false
	}
	n := now.In(c.Location)
	mins := n.Hour()*60 + n.Minute()
	start, ok1 := parseHM(c.QuietStart)
	end, ok2 := parseHM(c.QuietEnd)
	if !ok1 || !ok2 {
		return false
	}
	if start <= end {
		return mins >= start && mins < end
	}
	return mins >= start || mins < end // window crosses midnight
}

func parseHM(s string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	return h*60 + m, true
}

// Notifier sends events and remembers what it has already sent.
type Notifier struct {
	Cfg    Config
	Client *http.Client
	Seen   *SeenStore
	Now    func() time.Time
	Log    func(string)
	// DryRun prints instead of sending. The pipeline's default everywhere.
	DryRun bool
}

func New(root string, cfg Config) *Notifier {
	return &Notifier{
		Cfg:    cfg,
		Client: &http.Client{Timeout: 15 * time.Second},
		Seen:   NewSeenStore(filepath.Join(root, "state", "notified.json")),
		Now:    time.Now,
	}
}

func (n *Notifier) logf(f string, a ...any) {
	if n.Log != nil {
		n.Log(fmt.Sprintf(f, a...))
	}
}

// Send delivers one event, unless it is a duplicate or quiet hours apply.
func (n *Notifier) Send(ctx context.Context, e Event) (bool, error) {
	if !n.Cfg.Configured() {
		n.logf("    notify: no topic configured; would have sent %q", e.Title)
		return false, nil
	}
	if e.DedupeKey != "" && n.Seen.Has(e.DedupeKey) {
		return false, nil
	}
	now := n.Now()
	if e.Priority < PriorityHigh && n.Cfg.Quiet(now) {
		n.logf("    notify: quiet hours, holding %q for the digest", e.Title)
		return false, nil
	}
	if n.DryRun {
		fmt.Printf("[dry-run] notify p%d %s: %s\n", e.Priority, e.Title, firstLine(e.Body))
		return false, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		n.Cfg.Server+"/"+n.Cfg.Topic, strings.NewReader(e.Body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Title", oneLine(e.Title))
	req.Header.Set("Priority", fmt.Sprint(int(e.Priority)))
	if len(e.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(e.Tags, ","))
	}
	if e.URL != "" {
		req.Header.Set("Click", e.URL)
	}
	if hdr := n.actionsHeader(e); hdr != "" {
		req.Header.Set("Actions", hdr)
	}

	resp, err := n.Client.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrNotify, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, fmt.Errorf("%w: %s returned %s", ErrNotify, n.Cfg.Server, resp.Status)
	}
	if e.DedupeKey != "" {
		n.Seen.Mark(e.DedupeKey, now)
	}
	return true, nil
}

// actionsHeader renders ntfy action buttons.
//
// Commands go to a SECOND topic, not the one notifications arrive on, so
// someone who sees a screenshot of a notification gains read access and
// nothing more. Each command is signed and timestamped; `halt` deliberately
// is not, because failing open in the direction of "stop" is the safe one.
func (n *Notifier) actionsHeader(e Event) string {
	var parts []string
	for _, a := range e.Actions {
		switch {
		case a.Verb == "view" && a.URL != "":
			parts = append(parts, fmt.Sprintf("view, %s, %s, clear=true", a.Label, a.URL))
		case n.Cfg.CommandTopic != "":
			body := a.Verb
			if a.Arg != "" {
				body += " " + a.Arg
			}
			parts = append(parts, fmt.Sprintf("http, %s, %s/%s, method=POST, body=%q, clear=true",
				a.Label, n.Cfg.Server, n.Cfg.CommandTopic, body))
		}
	}
	return strings.Join(parts, "; ")
}

// SeenStore remembers delivered notifications so an hourly poll does not
// re-announce the same thing.
//
// Deliberately separate from the watcher's own seen-list: that one is only
// written when the run is executing, and a notification whose delivery failed
// must be retried even though the underlying item was already processed.
type SeenStore struct {
	path    string
	entries map[string]seenEntry
}

type seenEntry struct {
	At    string `json:"at"`
	Count int    `json:"count"`
}

func NewSeenStore(path string) *SeenStore {
	s := &SeenStore{path: path, entries: map[string]seenEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.entries)
	}
	return s
}

func (s *SeenStore) Has(key string) bool { _, ok := s.entries[key]; return ok }

func (s *SeenStore) Mark(key string, now time.Time) {
	e := s.entries[key]
	e.At = now.UTC().Format(time.RFC3339)
	e.Count++
	s.entries[key] = e
	s.flush()
}

// Prune drops entries older than ttl so the file cannot grow without bound.
func (s *SeenStore) Prune(now time.Time, ttl time.Duration) {
	for k, e := range s.entries {
		if t, err := time.Parse(time.RFC3339, e.At); err == nil && now.Sub(t) > ttl {
			delete(s.entries, k)
		}
	}
	s.flush()
}

func (s *SeenStore) flush() {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// Keys returns the remembered keys, sorted. For tests and `doctor`.
func (s *SeenStore) Keys() []string {
	out := make([]string, 0, len(s.entries))
	for k := range s.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", ""))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
