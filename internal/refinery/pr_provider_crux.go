package refinery

import (
	"errors"
	"fmt"

	"github.com/steveyegge/gastown/internal/crux"
	"github.com/steveyegge/gastown/internal/git"
)

// errCruxApprovalUnsupported is surfaced when require_review=true is configured
// against vcs_provider=amazon. CRUX's `cr` CLI is a creation/update tool only;
// there is no public API to poll review approval state, so the refinery cannot
// gate merges on approvals in this initial implementation. Operators must set
// require_review=false for now.
var errCruxApprovalUnsupported = errors.New(
	"vcs_provider=amazon does not yet support require_review=true — " +
		"set require_review=false to let CRUX handle approval via --auto-merge",
)

// cruxPRProvider implements PRProvider for Amazon CRUX code reviews.
//
// CRUX differs from GitHub/Bitbucket in a few important ways:
//
//   - There is no public REST API for querying or merging reviews — everything
//     goes through the `cr` CLI on the host.
//   - `cr` is a creation/update tool, not a query tool. We cannot list reviews
//     by branch. Instead we rely on the CR URL trailer that `cr` writes into
//     commit messages via `--amend`, and scan the branch log for it.
//   - Merges are asynchronous: `cr --publish --auto-merge` marks a review as
//     eligible for merge; CRUX merges it once required approvals land. The
//     refinery therefore does not observe a synchronous merge commit SHA.
type cruxPRProvider struct {
	git *git.Git
	pkg string
}

// newCruxPRProvider constructs a CRUX-backed PRProvider. It validates that the
// repo's origin remote is an Amazon git.amazon.com URL so misconfigurations
// surface at startup rather than at merge time.
func newCruxPRProvider(g *git.Git) (PRProvider, error) {
	remoteURL, err := g.RemoteURL("origin")
	if err != nil {
		return nil, fmt.Errorf("crux provider: failed to get origin remote URL: %w", err)
	}
	pkg, err := crux.ParseAmazonRemote(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("crux provider: %w", err)
	}
	return &cruxPRProvider{git: g, pkg: pkg}, nil
}

// FindPRNumber returns the numeric CR ID for the branch, inferred from a
// code.amazon.com/reviews/CR-<N> trailer in the branch's recent commits.
// Returns 0 if no CR is associated with the branch yet — callers should
// create one via MergePR in that case.
func (p *cruxPRProvider) FindPRNumber(branch string) (int, error) {
	return p.git.FindCruxCRID(branch)
}

// IsPRApproved is not supported by CRUX in this initial implementation because
// `cr` does not expose review state and there is no public REST API. Operators
// who want human review should set require_review=false and rely on CRUX's
// server-side approval rules, which are honored by --auto-merge.
func (p *cruxPRProvider) IsPRApproved(prNumber int) (bool, error) {
	_ = prNumber
	return false, errCruxApprovalUnsupported
}

// MergePR creates or updates a CRUX review with --publish --auto-merge so
// CRUX merges the change as soon as required approvals are granted.
//
// The returned merge-commit SHA is always empty: CRUX merges are asynchronous
// and the refinery cannot observe the merge commit at call time. The caller's
// post-merge `git pull` picks up whatever landed on mainline.
//
// The method parameter is accepted for interface compatibility but ignored —
// CRUX controls the merge strategy server-side.
func (p *cruxPRProvider) MergePR(prNumber int, method string) (string, error) {
	_ = method
	// Build the cr command args. When prNumber is 0 we create a new review.
	// Summary/description are intentionally blank here because the refinery
	// does not have bead context at this layer; `cr` falls back to the branch
	// commit message which is the canonical source of truth for the change.
	args := crux.BuildCreateArgs(prNumber, "", "", "")
	newID, output, err := p.git.CruxCRCreate(args)
	if err != nil {
		return "", fmt.Errorf("crux: cr invocation failed (pkg=%s, cr=%d): %w", p.pkg, prNumber, err)
	}
	// Prefer the updated ID (for new reviews) but fall back to the input
	// when output parsing fails, so logs still reflect the intended review.
	_ = output
	if newID == 0 && prNumber > 0 {
		newID = prNumber
	}
	_ = newID // currently only used implicitly via log/trailer; returning "" SHA is correct.
	// CRUX does not surface a merge commit SHA synchronously.
	return "", nil
}
