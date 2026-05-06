package cmd

import "testing"

func TestMatchesGhPrCreate(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"gh pr create", "gh pr create --title foo", true},
		{"gh pr create bare", "gh pr create", true},
		{"gh pr list", "gh pr list", false},
		{"gh pr view", "gh pr view 123", false},
		{"echo gh pr create", "echo gh pr create", true}, // still matches tokens
		{"no gh", "git push origin main", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesGhPrCreate(tt.command); got != tt.want {
				t.Errorf("matchesGhPrCreate(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestMatchesGitNewBranch(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"git checkout -b foo", "git checkout -b feature-x", true},
		{"git switch -c foo", "git switch -c feature-x", true},
		{"git checkout main", "git checkout main", false},
		{"git switch main", "git switch main", false},
		{"git checkout existing", "git checkout some-branch", false},
		{"git branch -d", "git branch -d old-branch", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesGitNewBranch(tt.command); got != tt.want {
				t.Errorf("matchesGitNewBranch(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestMatchesPRWorkflowPattern(t *testing.T) {
	tests := []struct {
		name    string
		command string
		blocked bool
	}{
		// Should block
		{"gh pr create", "gh pr create --title x", true},
		{"git checkout -b", "git checkout -b foo", true},
		{"git switch -c", "git switch -c new-feature", true},

		// Should allow
		{"ls /tmp", "ls /tmp", false},
		{"gt prime", "gt prime", false},
		{"cat file", "cat README.md", false},
		{"git checkout main", "git checkout main", false},
		{"git switch main", "git switch main", false},
		{"git push origin main", "git push origin main", false},
		{"gh pr list", "gh pr list", false},
		{"echo hello", "echo hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesPRWorkflowPattern(tt.command) != ""
			if got != tt.blocked {
				t.Errorf("matchesPRWorkflowPattern(%q) blocked=%v, want %v", tt.command, got, tt.blocked)
			}
		})
	}
}

func TestExtractCommandFromHookInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"valid", `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`, "ls -la"},
		{"empty", "", ""},
		{"invalid json", "not json", ""},
		{"no command", `{"tool_name":"Write","tool_input":{"file_path":"/tmp"}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractCommandFromHookInput([]byte(tt.input)); got != tt.want {
				t.Errorf("extractCommandFromHookInput() = %q, want %q", got, tt.want)
			}
		})
	}
}
