package polecat

import "testing"

func TestIsEnvironmentalPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// Allowlist hits — exact filename.
		{"gitignore root", ".gitignore", true},
		{"gitignore nested", "apps/web/.gitignore", true},
		{"package-lock root", "package-lock.json", true},
		{"package-lock nested", "services/api/package-lock.json", true},
		{"yarn lock", "yarn.lock", true},
		{"pnpm lock", "pnpm-lock.yaml", true},
		// Allowlist hits — directory prefix.
		{"build dir root", "build/x86_64/foo.o", true},
		{"build dir nested", "packages/bar/build/output.txt", true},
		{"dist nested", "tools/dist/index.js", true},
		{"runtime", ".runtime/state.json", true},
		{"claude state", ".claude/sessions/abc.json", true},
		{"node_modules", "node_modules/lodash/index.js", true},
		{"node_modules nested", "frontend/node_modules/foo/x.js", true},
		{"pycache", "src/__pycache__/foo.pyc", true},
		{"vscode", ".vscode/settings.json", true},
		// Backslash normalization.
		{"backslash path", "frontend\\node_modules\\x\\index.js", true},
		// Misses — real source files.
		{"go source", "internal/cmd/foo.go", false},
		{"readme", "README.md", false},
		{"gitignore-similar but not", "gitignore.bak", false},
		{"package.json (not lock)", "package.json", false},
		{"build but a real file named build", "src/build.go", false},
		{"pseudo build prefix", "buildtools/foo.go", false},
		// Empty / odd.
		{"empty", "", false},
		{"dot slash gitignore", "./.gitignore", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEnvironmentalPath(tt.path); got != tt.want {
				t.Errorf("IsEnvironmentalPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsEnvironmentalOnlyStash(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  bool
	}{
		{
			name:  "all gitignore + lockfile (the bug-report case)",
			paths: []string{".gitignore", "package-lock.json"},
			want:  true,
		},
		{
			name:  "single gitignore",
			paths: []string{".gitignore"},
			want:  true,
		},
		{
			name:  "all build outputs",
			paths: []string{"build/x.o", "dist/y.js", ".runtime/z"},
			want:  true,
		},
		{
			name:  "mixed — one real source file forbids drop",
			paths: []string{".gitignore", "internal/cmd/foo.go"},
			want:  false,
		},
		{
			name:  "all real source",
			paths: []string{"foo.go", "bar.go"},
			want:  false,
		},
		{
			name:  "empty paths list — anomalous, do not drop",
			paths: nil,
			want:  false,
		},
		{
			name:  "empty path string in list — treat as non-environmental",
			paths: []string{".gitignore", ""},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEnvironmentalOnlyStash(tt.paths); got != tt.want {
				t.Errorf("IsEnvironmentalOnlyStash(%v) = %v, want %v", tt.paths, got, tt.want)
			}
		})
	}
}

func TestIsEnvironmentalOnlyStash_CustomAllowlist(t *testing.T) {
	// Verify the allowlist parameter actually plumbs through — protects against
	// the policy diverging from the testable predicate.
	allow := []string{"only_this.txt", "vendor/"}
	if !IsEnvironmentalOnlyStashWithAllowlist([]string{"only_this.txt", "vendor/foo/bar"}, allow) {
		t.Error("expected env-only with custom allowlist")
	}
	if IsEnvironmentalOnlyStashWithAllowlist([]string{"only_this.txt", ".gitignore"}, allow) {
		t.Error(".gitignore should not match a non-default allowlist")
	}
}
