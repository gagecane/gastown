package ciwatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// GHRunFetcher implements RunFetcher using the `gh` CLI. The CLI is the path
// of least resistance: it handles auth (host config / GITHUB_TOKEN), pagination
// (--limit), and JSON output (--json) without us having to reproduce any of
// it. Production polecats already have `gh` installed.
//
// The fetcher is intentionally simple — no caching, no retry. The watcher
// runs one-shot and re-invokes from scheduled patrols.
type GHRunFetcher struct {
	// WorkDir is the directory `gh` runs in. Required so `gh` resolves the
	// repo from the local git remote rather than relying on cwd.
	WorkDir string

	// Bin is the executable name; defaults to "gh".
	Bin string
}

// NewGHRunFetcher constructs a GHRunFetcher.
func NewGHRunFetcher(workDir string) *GHRunFetcher {
	return &GHRunFetcher{WorkDir: workDir, Bin: "gh"}
}

// ghRunListEntry is the JSON shape returned by `gh run list --json ...`.
// We pull only the fields we need; gh ignores unknown --json keys.
type ghRunListEntry struct {
	DatabaseID int64  `json:"databaseId"`
	HeadBranch string `json:"headBranch"`
	HeadSHA    string `json:"headSha"`
	// gh's --json field for the title is the workflow name; it does NOT
	// include the commit message. We populate HeadCommitSubject below via
	// `git log -1 --format=%s <sha>`.
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
	CreatedAt   time.Time `json:"createdAt"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"-"` // synthetic from updatedAt when status=completed
}

// CompletedRuns shells out to `gh run list` and returns the most recent
// completed runs on `branch`, newest first.
func (g *GHRunFetcher) CompletedRuns(ctx context.Context, branch string, limit int) ([]CIRun, error) {
	if branch == "" {
		branch = "main"
	}
	if limit <= 0 {
		limit = DefaultRunLimit
	}
	bin := g.Bin
	if bin == "" {
		bin = "gh"
	}
	args := []string{
		"run", "list",
		"--branch", branch,
		"--status", "completed",
		"--limit", strconv.Itoa(limit),
		"--json", "databaseId,headBranch,headSha,name,status,conclusion,url,updatedAt,createdAt,startedAt",
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is operator-controlled
	if g.WorkDir != "" {
		cmd.Dir = g.WorkDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh run list: %w (stderr: %s)", err, stderr.String())
	}

	var entries []ghRunListEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("gh run list: parse json: %w", err)
	}

	runs := make([]CIRun, 0, len(entries))
	for _, e := range entries {
		runs = append(runs, CIRun{
			ID:                strconv.FormatInt(e.DatabaseID, 10),
			HeadSHA:           e.HeadSHA,
			HeadCommitSubject: g.fetchSubject(ctx, e.HeadSHA),
			Conclusion:        mapGHConclusion(e.Conclusion),
			CompletedAt:       e.UpdatedAt,
			URL:               e.URL,
			Workflow:          e.Name,
			Branch:            e.HeadBranch,
		})
	}
	return runs, nil
}

// fetchSubject runs `git log -1 --format=%s <sha>` to grab the commit subject.
// Best-effort: if we can't resolve it (commit fetched but not yet local),
// return an empty subject and rely on the bead-extractor to no-op cleanly.
func (g *GHRunFetcher) fetchSubject(ctx context.Context, sha string) string {
	if sha == "" {
		return ""
	}
	cmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%s", sha)
	if g.WorkDir != "" {
		cmd.Dir = g.WorkDir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	// strip trailing newline
	s := out.String()
	if n := len(s); n > 0 && s[n-1] == '\n' {
		s = s[:n-1]
	}
	return s
}

// mapGHConclusion translates GitHub Actions' conclusion strings to our enum.
// See https://docs.github.com/en/rest/actions/workflow-runs for the canonical
// list. Unrecognized values map to ConclusionUnknown so the watcher logs but
// does not freeze.
func mapGHConclusion(s string) Conclusion {
	switch s {
	case "success":
		return ConclusionSuccess
	case "failure":
		return ConclusionFailure
	case "cancelled":
		return ConclusionCancelled
	case "timed_out":
		return ConclusionTimedOut
	case "startup_failure":
		return ConclusionStartupFailure
	}
	return ConclusionUnknown
}
