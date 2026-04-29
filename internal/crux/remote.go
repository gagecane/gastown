// Package crux provides integration with Amazon CRUX code reviews via the
// `cr` CLI for repositories hosted on git.amazon.com.
//
// Unlike GitHub and Bitbucket, CRUX does not expose a public REST API
// suitable for automated polling, and the `cr` CLI is a creation/update
// tool rather than a query tool. This package therefore focuses on the
// operations the refinery actually needs for merge_strategy=pr:
//
//   - parse the package name out of a git.amazon.com remote URL,
//   - extract a CR ID from branch commit messages, and
//   - create or update a CR with auto-merge enabled via `cr`.
//
// Approval checking is not implemented in this initial version —
// require_review=true is unsupported and surfaced as a clear error.
package crux

import (
	"fmt"
	"strings"
)

// ParseAmazonRemote extracts the package name from an Amazon git.amazon.com
// remote URL. It supports the standard formats used by internal repositories:
//
//	ssh://git.amazon.com/pkg/PackageName
//	ssh://git.amazon.com/pkg/PackageName.git
//	ssh://git.amazon.com/pkg/PackageName/  (trailing slash)
//
// The scheme may also be plain git.amazon.com:pkg/PackageName style SSH.
//
// Non-Amazon URLs return an error. Multi-segment package paths
// (ssh://git.amazon.com/pkg/OrgName/PackageName) are preserved verbatim
// so callers can pass the full path to `cr` if needed.
func ParseAmazonRemote(remoteURL string) (pkg string, err error) {
	var rest string
	switch {
	case strings.HasPrefix(remoteURL, "ssh://git.amazon.com/pkg/"):
		rest = strings.TrimPrefix(remoteURL, "ssh://git.amazon.com/pkg/")
	case strings.HasPrefix(remoteURL, "git.amazon.com:pkg/"):
		rest = strings.TrimPrefix(remoteURL, "git.amazon.com:pkg/")
	case strings.HasPrefix(remoteURL, "https://git.amazon.com/pkg/"):
		rest = strings.TrimPrefix(remoteURL, "https://git.amazon.com/pkg/")
	default:
		return "", fmt.Errorf("crux: not a git.amazon.com pkg URL: %s", remoteURL)
	}

	rest = strings.TrimSuffix(rest, ".git")
	rest = strings.TrimSuffix(rest, "/")

	if rest == "" {
		return "", fmt.Errorf("crux: cannot parse package name from: %s", remoteURL)
	}

	// Multi-segment paths are passed through verbatim; `cr` can resolve
	// them via the workspace/destination-branch arguments.
	return rest, nil
}
