package refinery

import "github.com/steveyegge/gastown/internal/git"

// githubPRProvider implements PRProvider using the gh CLI via git.Git.
type githubPRProvider struct {
	git *git.Git
}

func newGitHubPRProvider(g *git.Git) PRProvider {
	return &githubPRProvider{git: g}
}

func (p *githubPRProvider) FindPRNumber(branch string) (int, error) {
	return p.git.FindPRNumber(branch)
}

func (p *githubPRProvider) IsPRApproved(prNumber int) (bool, error) {
	return p.git.IsPRApproved(prNumber)
}

// GetPRChecks implements the optional prChecksProvider capability (gs-vlyt),
// reporting the PR's CI check state so the github-checks gate can pass on green
// checks instead of running CI locally.
func (p *githubPRProvider) GetPRChecks(prNumber int, requiredOnly bool) ([]PRCheck, error) {
	runs, err := p.git.GetPRChecks(prNumber, requiredOnly)
	if err != nil {
		return nil, err
	}
	checks := make([]PRCheck, len(runs))
	for i, r := range runs {
		checks[i] = PRCheck{Name: r.Name, State: r.State, Bucket: r.Bucket}
	}
	return checks, nil
}

// FindMergedPRCommit implements the optional mergedPRFinder capability (gs-4uz).
func (p *githubPRProvider) FindMergedPRCommit(branch string) (string, error) {
	return p.git.FindMergedPRCommit(branch)
}

func (p *githubPRProvider) MergePR(prNumber int, method string) (string, error) {
	return p.git.GhPrMerge(prNumber, method)
}
