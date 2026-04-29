package crux

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAmazonRemote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		pkg     string
		wantErr bool
	}{
		{
			name: "ssh form",
			url:  "ssh://git.amazon.com/pkg/MyPackage",
			pkg:  "MyPackage",
		},
		{
			name: "ssh form with trailing slash",
			url:  "ssh://git.amazon.com/pkg/MyPackage/",
			pkg:  "MyPackage",
		},
		{
			name: "ssh form with .git suffix",
			url:  "ssh://git.amazon.com/pkg/MyPackage.git",
			pkg:  "MyPackage",
		},
		{
			name: "ssh-colon form",
			url:  "git.amazon.com:pkg/MyPackage",
			pkg:  "MyPackage",
		},
		{
			name: "https form",
			url:  "https://git.amazon.com/pkg/MyPackage",
			pkg:  "MyPackage",
		},
		{
			name: "multi-segment package path preserved",
			url:  "ssh://git.amazon.com/pkg/OrgName/PackageName",
			pkg:  "OrgName/PackageName",
		},
		{
			name:    "github URL rejected",
			url:     "https://github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "bitbucket URL rejected",
			url:     "https://bitbucket.org/ws/repo",
			wantErr: true,
		},
		{
			name:    "empty URL rejected",
			url:     "",
			wantErr: true,
		},
		{
			name:    "missing package name",
			url:     "ssh://git.amazon.com/pkg/",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pkg, err := ParseAmazonRemote(tc.url)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.pkg, pkg)
		})
	}
}
