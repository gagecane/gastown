package prwatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrPRsUnavailable signals that the host has no pollable PRs for the rig: the
// repo does not exist (e.g. a fork that was never created) or access is denied.
// GitHub returns HTTP 404 in both cases. This is a benign, persistent condition
// — not a transient fetch failure — so the watcher treats it as "nothing to
// poll" and skips the rig (mirrors ciwatcher.ErrRunsUnavailable).
var ErrPRsUnavailable = errors.New("prwatcher: PR endpoint unavailable (repo missing or access denied)")

// GHCommentFetcher implements CommentFetcher using the `gh` CLI's GraphQL API.
// Review threads (with their isResolved flag) are only exposed via GraphQL, so
// we issue a single `gh api graphql` query that walks open PRs → review threads
// → comments and returns the comments on UNRESOLVED threads.
type GHCommentFetcher struct {
	// WorkDir is the directory `gh` runs in. Required so `gh` resolves the repo
	// from the local git remote.
	WorkDir string

	// Owner/Repo pin the GraphQL query to the rig's origin repo. When empty the
	// fetcher resolves them from the origin remote (see resolveRepo).
	Owner string
	Repo  string

	// Bin is the executable name; defaults to "gh".
	Bin string

	// PRLimit caps how many open PRs to inspect; defaults to 30.
	PRLimit int

	// ThreadLimit caps review threads per PR; defaults to 50.
	ThreadLimit int
}

// NewGHCommentFetcher constructs a GHCommentFetcher anchored at workDir.
func NewGHCommentFetcher(workDir string) *GHCommentFetcher {
	return &GHCommentFetcher{WorkDir: workDir, Bin: "gh", PRLimit: 30, ThreadLimit: 50}
}

// graphqlQuery walks open PRs and their review threads. We pull the first
// comment of each unresolved thread (the thread's root comment is the
// reviewer's actionable request; replies are usually discussion).
const graphqlQuery = `query($owner:String!,$repo:String!,$prLimit:Int!,$threadLimit:Int!){
  repository(owner:$owner,name:$repo){
    pullRequests(states:OPEN,first:$prLimit,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{
        number
        title
        reviewThreads(first:$threadLimit){
          nodes{
            isResolved
            isOutdated
            comments(first:1){
              nodes{ id author{login} body path line createdAt url }
            }
          }
        }
      }
    }
  }
}`

// graphqlResponse mirrors the JSON shape returned by the query above.
type graphqlResponse struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number        int    `json:"number"`
					Title         string `json:"title"`
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
							IsOutdated bool `json:"isOutdated"`
							Comments   struct {
								Nodes []struct {
									ID     string `json:"id"`
									Author struct {
										Login string `json:"login"`
									} `json:"author"`
									Body      string    `json:"body"`
									Path      string    `json:"path"`
									Line      int       `json:"line"`
									CreatedAt time.Time `json:"createdAt"`
									URL       string    `json:"url"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// UnresolvedComments issues the GraphQL query and returns the root comment of
// each unresolved, non-outdated review thread on every open PR, newest PR
// first. Outdated threads (the code they anchored to has since changed) are
// skipped: the comment may no longer apply and re-pushing against a moved line
// is unsafe.
func (g *GHCommentFetcher) UnresolvedComments(ctx context.Context) ([]ReviewComment, error) {
	owner, repo := g.Owner, g.Repo
	if owner == "" || repo == "" {
		o, r := g.resolveRepo(ctx)
		if o == "" || r == "" {
			return nil, fmt.Errorf("prwatcher: could not resolve origin repo (set Owner/Repo or run in a github clone)")
		}
		owner, repo = o, r
	}
	bin := g.Bin
	if bin == "" {
		bin = "gh"
	}
	prLimit := g.PRLimit
	if prLimit <= 0 {
		prLimit = 30
	}
	threadLimit := g.ThreadLimit
	if threadLimit <= 0 {
		threadLimit = 50
	}

	args := []string{
		"api", "graphql",
		"-f", "query=" + graphqlQuery,
		"-F", "owner=" + owner,
		"-F", "repo=" + repo,
		"-F", fmt.Sprintf("prLimit=%d", prLimit),
		"-F", fmt.Sprintf("threadLimit=%d", threadLimit),
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin operator-controlled
	if g.WorkDir != "" {
		cmd.Dir = g.WorkDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isNotFoundErr(stderr.String()) {
			return nil, fmt.Errorf("%w (repo=%s/%s, stderr: %s)", ErrPRsUnavailable, owner, repo, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("gh api graphql: %w (stderr: %s)", err, stderr.String())
	}

	var resp graphqlResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("gh api graphql: parse json: %w", err)
	}

	var out []ReviewComment
	for _, pr := range resp.Data.Repository.PullRequests.Nodes {
		for _, th := range pr.ReviewThreads.Nodes {
			if th.IsResolved || th.IsOutdated {
				continue
			}
			if len(th.Comments.Nodes) == 0 {
				continue
			}
			cn := th.Comments.Nodes[0]
			out = append(out, ReviewComment{
				ID:        cn.ID,
				PRNumber:  pr.Number,
				PRTitle:   pr.Title,
				Author:    cn.Author.Login,
				Body:      cn.Body,
				Path:      cn.Path,
				Line:      cn.Line,
				URL:       cn.URL,
				CreatedAt: cn.CreatedAt,
			})
		}
	}
	return out, nil
}

// resolveRepo resolves the `origin` remote to (owner, repo). Returns empty
// strings when origin is absent or unparseable.
func (g *GHCommentFetcher) resolveRepo(ctx context.Context) (string, string) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	if g.WorkDir != "" {
		cmd.Dir = g.WorkDir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", ""
	}
	return parseGitHubOwnerRepo(strings.TrimSpace(out.String()))
}

// parseGitHubOwnerRepo extracts (owner, repo) from a GitHub remote URL. Supports
// HTTPS and SSH forms. Returns empty strings for any non-GitHub remote.
func parseGitHubOwnerRepo(remoteURL string) (string, string) {
	var path string
	switch {
	case strings.HasPrefix(remoteURL, "https://github.com/"):
		path = strings.TrimPrefix(remoteURL, "https://github.com/")
	case strings.HasPrefix(remoteURL, "git@github.com:"):
		path = strings.TrimPrefix(remoteURL, "git@github.com:")
	default:
		return "", ""
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// isNotFoundErr reports whether a gh stderr indicates HTTP 404 — the repo does
// not exist or access is denied.
func isNotFoundErr(stderr string) bool {
	return strings.Contains(stderr, "Could not resolve to a Repository") ||
		strings.Contains(stderr, "HTTP 404")
}

// GHReplier implements Replier by shelling out to `gh pr comment`. The ack is
// posted as a top-level PR comment (not a threaded review reply): `gh` exposes
// `pr comment` simply, and a top-level ack is sufficient for the reviewer to
// see the work was picked up. Threaded replies would require the GraphQL
// addPullRequestReviewThreadReply mutation, which we can adopt later if a
// per-thread ack proves necessary.
type GHReplier struct {
	WorkDir string
	Owner   string
	Repo    string
	Bin     string
}

// NewGHReplier constructs a GHReplier anchored at workDir.
func NewGHReplier(workDir string) *GHReplier {
	return &GHReplier{WorkDir: workDir, Bin: "gh"}
}

// Reply posts an ack comment on the PR. Body is passed via --body-file - (stdin)
// to avoid quoting issues.
func (r *GHReplier) Reply(prNumber int, body string) error {
	bin := r.Bin
	if bin == "" {
		bin = "gh"
	}
	args := []string{"pr", "comment", fmt.Sprintf("%d", prNumber), "--body-file", "-"}
	if r.Owner != "" && r.Repo != "" {
		args = append(args, "--repo", r.Owner+"/"+r.Repo)
	}
	cmd := exec.Command(bin, args...) //nolint:gosec // bin operator-controlled
	if r.WorkDir != "" {
		cmd.Dir = r.WorkDir
	}
	cmd.Stdin = strings.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr comment #%d: %w (stderr: %s)", prNumber, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
