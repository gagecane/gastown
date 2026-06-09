package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMatchesMRSourceIssue(t *testing.T) {
	tests := []struct {
		name        string
		description string
		issueID     string
		want        bool
	}{
		{
			name:        "exact match",
			description: "branch: polecat/furiosa/gt-abc@mm4heq3e\ntarget: main\nsource_issue: gt-abc\nrig: gastown\n",
			issueID:     "gt-abc",
			want:        true,
		},
		{
			name:        "no match different issue",
			description: "branch: polecat/furiosa/gt-xyz@mm4heq3e\ntarget: main\nsource_issue: gt-xyz\nrig: gastown\n",
			issueID:     "gt-abc",
			want:        false,
		},
		{
			name:        "partial ID must not match — prefix",
			description: "branch: polecat/nux/gt-abcdef@mm4heq3e\ntarget: main\nsource_issue: gt-abcdef\nrig: gastown\n",
			issueID:     "gt-abc",
			want:        false,
		},
		{
			name:        "partial ID must not match — suffix",
			description: "branch: polecat/nux/gt-abc@mm4heq3e\ntarget: main\nsource_issue: gt-abc\nrig: gastown\n",
			issueID:     "gt-abcdef",
			want:        false,
		},
		{
			name:        "match with worker field after source_issue",
			description: "branch: polecat/furiosa/la-cagb2@mm4heq3e\ntarget: main\nsource_issue: la-cagb2\nworker: polecats/furiosa\n",
			issueID:     "la-cagb2",
			want:        true,
		},
		{
			name:        "source_issue at end of description (with trailing newline)",
			description: "branch: fix/thing\nsource_issue: gt-99\n",
			issueID:     "gt-99",
			want:        true,
		},
		{
			name:        "source_issue at end without trailing newline — no match",
			description: "branch: fix/thing\nsource_issue: gt-99",
			issueID:     "gt-99",
			want:        false,
		},
		{
			name:        "empty description",
			description: "",
			issueID:     "gt-abc",
			want:        false,
		},
		{
			name:        "empty issue ID",
			description: "source_issue: gt-abc\n",
			issueID:     "",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesMRSourceIssue(tt.description, tt.issueID)
			if got != tt.want {
				t.Errorf("MatchesMRSourceIssue(%q, %q) = %v, want %v",
					tt.description, tt.issueID, got, tt.want)
			}
		})
	}
}

// TestRepointSupersededMRAgent covers the gs-stvm fix: when an MR is superseded,
// the superseded MR's owning agent bead must be re-pointed to the new MR so the
// post-merge orphan reconcile and `gt polecat nuke` follow the MR that actually
// merges instead of the closed superseded one.
func TestRepointSupersededMRAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell mock for bd")
	}

	t.Run("repoints owning agent bead to new MR", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		agentShow := `[{"id":"gt-gastown-polecat-dead","title":"Polecat dead","issue_type":"agent","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: done\nactive_mr: mr-old"}]`
		logPath := installMockBDShowRecorder(t, agentShow)

		bd := NewIsolated(tmpDir)
		oldMR := &Issue{
			ID:          "mr-old",
			Description: "branch: polecat/dead/gs-1\nsource_issue: gs-1\nagent_bead: gt-gastown-polecat-dead\n",
		}
		if err := bd.RepointSupersededMRAgent(oldMR, "mr-new"); err != nil {
			t.Fatalf("RepointSupersededMRAgent: %v", err)
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read mock bd log: %v", err)
		}
		log := string(data)
		if !strings.Contains(log, "update") || !strings.Contains(log, "mr-new") {
			t.Fatalf("expected an update call setting active_mr=mr-new, got log:\n%s", log)
		}
		if !strings.Contains(log, "gt-gastown-polecat-dead") {
			t.Fatalf("expected update to target the owning agent bead, got log:\n%s", log)
		}
	})

	t.Run("no-op when MR carries no agent_bead", func(t *testing.T) {
		bd := NewIsolated(t.TempDir())
		oldMR := &Issue{ID: "mr-old", Description: "branch: b\nsource_issue: gs-1\n"}
		if err := bd.RepointSupersededMRAgent(oldMR, "mr-new"); err != nil {
			t.Fatalf("expected nil for MR without agent_bead, got %v", err)
		}
	})

	t.Run("no-op for nil MR", func(t *testing.T) {
		bd := NewIsolated(t.TempDir())
		if err := bd.RepointSupersededMRAgent(nil, "mr-new"); err != nil {
			t.Fatalf("expected nil for nil MR, got %v", err)
		}
	})
}
