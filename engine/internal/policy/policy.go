// Package policy loads the operational limits and scoring rules.
//
// Read fresh on each call (cached on file modification time) so a tuning edit
// takes effect on the next scheduled run without a restart.
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Caps struct {
	PRsPerDay           int `yaml:"prs_per_day"`
	PRsPerWeek          int `yaml:"prs_per_week"`
	MaxOpenPRs          int `yaml:"max_open_prs"`
	MaxOpenPerRepo      int `yaml:"max_open_per_repo"`
	OrgCooldownDays     int `yaml:"org_cooldown_days"`
	MaxCandidatesPerRun int `yaml:"max_candidates_per_run"`
	ReconsiderAfterDays int `yaml:"reconsider_rejected_after_days"`
	MaxHarvestPerRun    int `yaml:"max_harvest_per_run"`
	// Autonomous PRs get a strict subset of the overall budget, so a bug in
	// the gate cannot consume the whole allowance.
	AutoPRsPerDay  int `yaml:"auto_prs_per_day"`
	AutoPRsPerWeek int `yaml:"auto_prs_per_week"`
	MaxOpenAutoPRs int `yaml:"max_open_auto_prs"`
}

// Autonomy is the operator's standing authorisation for the pipeline to
// approve its own proposals.
//
// Standing rather than a countersigned grant that expires: the operator has
// said they will never approve proposals by hand, so an authorisation that
// lapses without renewal would stop the pipeline silently rather than make
// it safer. The edit to this file is the authorisation, it is re-read on
// every run, and setting mode back to "off" or engaging HALT stops the next
// approval.
type Autonomy struct {
	// Mode is "off" (every proposal waits for a person) or "standing".
	Mode string `yaml:"mode"`
	// Since is the date the operator switched it on, kept so the audit log
	// can say from when.
	Since string `yaml:"since"`
	// Trial runs autogate's silent period from Since before acting.
	Trial bool `yaml:"trial"`
	// Probation is the per-domain merge record required before acting. Zero
	// waives it, which is required when nobody approves by hand: the record
	// could otherwise never start.
	ProbationMinDecided int     `yaml:"probation_min_decided"`
	ProbationMinRate    float64 `yaml:"probation_min_rate"`
}

// Standing reports whether autonomous approval is switched on.
func (a Autonomy) Standing() bool { return a.Mode == "standing" }

type Staleness struct {
	AuthorSilentDays     int `yaml:"author_silent_days"`
	UnreviewedSilentDays int `yaml:"unreviewed_silent_days"`
	ChangesRequestedDays int `yaml:"changes_requested_days"`
	CIRedUntouchedDays   int `yaml:"ci_red_untouched_days"`
	ClaimHonouredDays    int `yaml:"claim_honoured_days"`
	ActivePRDays         int `yaml:"active_pr_days"`
	PRUntouchedDays      int `yaml:"pr_untouched_days"`
}

type Scoring struct {
	RequireConvergedThread      bool `yaml:"require_converged_thread"`
	RequireMaintainerAcceptance bool `yaml:"require_maintainer_acceptance"`
	RequireApproachOrCriteria   bool `yaml:"require_approach_or_criteria"`
	RequireTests                bool `yaml:"require_tests"`
	RequireContributing         bool `yaml:"require_contributing"`
	RejectDocsTypoOnly          bool `yaml:"reject_docs_typo_only"`
	RejectWorkflowChanges       bool `yaml:"reject_workflow_changes"`
	FirstTimeWindowDays         int  `yaml:"first_time_contributor_window_days"`
	StaleIssuePenaltyYears      int  `yaml:"stale_issue_penalty_years"`
}

type Policy struct {
	Caps             Caps      `yaml:"caps"`
	Autonomy         Autonomy  `yaml:"autonomy"`
	Staleness        Staleness `yaml:"staleness"`
	Scoring          Scoring   `yaml:"scoring"`
	AcceptanceLabels []string  `yaml:"acceptance_labels"`
	UntriagedLabels  []string  `yaml:"untriaged_labels"`
	Watchlist        struct {
		Tier            int     `yaml:"tier"`
		UnlockMergeRate float64 `yaml:"unlock_merge_rate"`
		UnlockMinPRs    int     `yaml:"unlock_min_prs"`
	} `yaml:"watchlist"`
}

type Exclusions struct {
	ExcludeRepos   []string `yaml:"exclude_repos"`
	BannedAIPolicy []string `yaml:"banned_ai_policy"`
	CLASigned      []string `yaml:"cla_signed"`
}

type Config struct {
	Policy     Policy
	Exclusions Exclusions
	// TouchedByOtherAccounts is the cached set of repos the user's other
	// GitHub accounts have contributed to. Two PRs on one issue from two
	// accounts reads as sockpuppeting even when it is innocent.
	TouchedByOtherAccounts map[string]bool
}

var (
	mu     sync.Mutex
	cached *Config
	stamp  [2]time.Time
)

func Load(root string) (*Config, error) {
	cfgDir := filepath.Join(root, "config")
	pPath := filepath.Join(cfgDir, "policy.yaml")
	ePath := filepath.Join(cfgDir, "exclusions.yaml")

	ps, err := os.Stat(pPath)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	es, err := os.Stat(ePath)
	if err != nil {
		return nil, fmt.Errorf("read exclusions: %w", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if cached != nil && stamp[0].Equal(ps.ModTime()) && stamp[1].Equal(es.ModTime()) {
		return cached, nil
	}

	var c Config
	if err := readYAML(pPath, &c.Policy); err != nil {
		return nil, err
	}
	if err := readYAML(ePath, &c.Exclusions); err != nil {
		return nil, err
	}
	c.TouchedByOtherAccounts = loadTouched(root)
	cached, stamp = &c, [2]time.Time{ps.ModTime(), es.ModTime()}
	return &c, nil
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

func loadTouched(root string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(root, "state", "cross_account_repos.json"))
	if err != nil {
		return out
	}
	var v struct {
		Repos []string `json:"repos"`
	}
	if json.Unmarshal(b, &v) != nil {
		return out
	}
	for _, r := range v.Repos {
		out[r] = true
	}
	return out
}

// Excluded reports whether a repo is on a denylist.
func (c *Config) Excluded(repo string) bool {
	for _, r := range c.Exclusions.ExcludeRepos {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

func (c *Config) CLASigned(repo string) bool {
	for _, r := range c.Exclusions.CLASigned {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}
