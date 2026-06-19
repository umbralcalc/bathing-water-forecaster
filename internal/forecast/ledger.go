package forecast

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LedgerName is the canonical filename for a commit: sortable by date, scoped by
// region, e.g. "2025-07-01_northumberland.json".
func LedgerName(region string, commit time.Time) string {
	return fmt.Sprintf("%s_%s.json", commit.Format("2006-01-02"), region)
}

// WriteCommit writes a commit to dir/<LedgerName>, refusing to overwrite an
// existing ledger so a committed forecast can never be silently rewritten — the
// whole point of proof-of-commit.
func WriteCommit(dir string, c Commit) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, LedgerName(c.Region, c.CommitTime))
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("forecast: ledger already exists, refusing to overwrite: %s", path)
	}
	if c.GeneratedAt.IsZero() {
		c.GeneratedAt = time.Now().UTC()
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}

// ReadCommit loads a commit ledger from a file.
func ReadCommit(path string) (Commit, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Commit{}, err
	}
	var c Commit
	if err := json.Unmarshal(b, &c); err != nil {
		return Commit{}, fmt.Errorf("forecast: parsing %s: %w", path, err)
	}
	return c, nil
}

// ResolvedLedger pairs a commit with its settled outcomes and the aggregate score.
type ResolvedLedger struct {
	Region      string       `json:"region"`
	CommitTime  time.Time    `json:"commitTime"`
	ResolvedAt  time.Time    `json:"resolvedAt"`
	Resolutions []Resolution `json:"resolutions"`
	Skipped     []string     `json:"skipped,omitempty"` // sites with no later/determinate sample
	Score       Score        `json:"score"`
}

// WriteResolved writes the resolved ledger alongside the commit, as
// "<commit>_<region>.resolved.json".
func WriteResolved(dir string, r ResolvedLedger) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s.resolved.json", r.CommitTime.Format("2006-01-02"), r.Region)
	path := filepath.Join(dir, name)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}
