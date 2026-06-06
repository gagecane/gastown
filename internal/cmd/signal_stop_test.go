package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/mail"
)

func TestIsSelfHandoff(t *testing.T) {
	tests := []struct {
		name    string
		msg     *mail.Message
		address string
		want    bool
	}{
		{
			name: "self handoff",
			msg: &mail.Message{
				From:    "gastown/crew/max",
				Subject: "🤝 HANDOFF: Session cycling",
			},
			address: "gastown/crew/max",
			want:    true,
		},
		{
			name: "handoff from other",
			msg: &mail.Message{
				From:    "gastown/crew/tom",
				Subject: "🤝 HANDOFF: Session cycling",
			},
			address: "gastown/crew/max",
			want:    false,
		},
		{
			name: "non-handoff from self",
			msg: &mail.Message{
				From:    "gastown/crew/max",
				Subject: "Regular message",
			},
			address: "gastown/crew/max",
			want:    false,
		},
		{
			name: "handoff keyword in subject",
			msg: &mail.Message{
				From:    "gastown/crew/max",
				Subject: "HANDOFF notes for next session",
			},
			address: "gastown/crew/max",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSelfHandoff(tt.msg, tt.address)
			if got != tt.want {
				t.Errorf("isSelfHandoff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSelfHandoffBead(t *testing.T) {
	tests := []struct {
		name string
		bead *beads.Issue
		want bool
	}{
		{
			name: "nil bead",
			bead: nil,
			want: false,
		},
		{
			name: "self-handoff mail bead",
			bead: &beads.Issue{
				Title:  "🤝 HANDOFF: Session cycling",
				Labels: []string{"gt:message", "from:gastown/polecats/shiny"},
			},
			want: true,
		},
		{
			name: "handoff keyword without leading emoji",
			bead: &beads.Issue{
				Title:  "HANDOFF notes for next session",
				Labels: []string{"gt:message"},
			},
			want: true,
		},
		{
			name: "genuine slung work (no message label)",
			bead: &beads.Issue{
				Title:  "Fix the auth bug",
				Labels: []string{"gt:bug"},
			},
			want: false,
		},
		{
			name: "message bead that is not a handoff",
			bead: &beads.Issue{
				Title:  "Question about the merge queue",
				Labels: []string{"gt:message"},
			},
			want: false,
		},
		{
			name: "handoff title but missing message label",
			bead: &beads.Issue{
				Title:  "🤝 HANDOFF: Session cycling",
				Labels: []string{"gt:task"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSelfHandoffBead(tt.bead)
			if got != tt.want {
				t.Errorf("isSelfHandoffBead() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSelfPatrolWisp(t *testing.T) {
	tests := []struct {
		name string
		bead *beads.Issue
		want bool
	}{
		{
			name: "nil bead",
			bead: nil,
			want: false,
		},
		{
			name: "witness patrol wisp",
			bead: &beads.Issue{
				Title: "mol-witness-patrol",
				Type:  "molecule",
			},
			want: true,
		},
		{
			name: "refinery patrol wisp",
			bead: &beads.Issue{
				Title: "mol-refinery-patrol",
				Type:  "molecule",
			},
			want: true,
		},
		{
			name: "deacon patrol wisp",
			bead: &beads.Issue{
				Title: "mol-deacon-patrol",
				Type:  "molecule",
			},
			want: true,
		},
		{
			name: "patrol wisp with trailing suffix in title",
			bead: &beads.Issue{
				Title: "mol-witness-patrol cycle 12",
				Type:  "molecule",
			},
			want: true,
		},
		{
			name: "genuine slung work bug",
			bead: &beads.Issue{
				Title: "Fix the auth bug",
				Type:  "bug",
			},
			want: false,
		},
		{
			name: "non-patrol molecule (real work)",
			bead: &beads.Issue{
				Title: "mol-implement-feature",
				Type:  "molecule",
			},
			want: false,
		},
		{
			name: "title merely contains patrol word but wrong prefix",
			bead: &beads.Issue{
				Title: "Investigate mol-witness-patrol hot-loop regression",
				Type:  "bug",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSelfPatrolWisp(tt.bead)
			if got != tt.want {
				t.Errorf("isSelfPatrolWisp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOutputStopAllow(t *testing.T) {
	// outputStopAllow should not return an error
	err := outputStopAllow()
	if err != nil {
		t.Errorf("outputStopAllow() returned error: %v", err)
	}
}

func TestOutputStopBlock(t *testing.T) {
	// outputStopBlock should not return an error
	err := outputStopBlock("test reason")
	if err != nil {
		t.Errorf("outputStopBlock() returned error: %v", err)
	}
}

func TestStopStateFilePath(t *testing.T) {
	got := stopStateFilePath("gastown/polecats/nux")
	want := filepath.Join(os.TempDir(), "gt-signal-stop-gastown_polecats_nux.json")
	if got != want {
		t.Errorf("stopStateFilePath() = %q, want %q", got, want)
	}
}

func TestStopStateRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-state.json")

	// Initially no state
	state := loadStopState(path)
	if state != nil {
		t.Fatal("expected nil state for missing file")
	}

	// Save state
	saveStopState(path, &stopState{LastReason: "you have mail"})

	// Load it back
	state = loadStopState(path)
	if state == nil {
		t.Fatal("expected non-nil state after save")
	}
	if state.LastReason != "you have mail" {
		t.Errorf("LastReason = %q, want %q", state.LastReason, "you have mail")
	}

	// Clear state
	clearStopState(path)
	state = loadStopState(path)
	if state != nil {
		t.Fatal("expected nil state after clear")
	}
}

func TestStopStateDedupPreventsInfiniteLoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-state.json")
	reason := "[gt signal stop] You have 1 unread message(s). Most recent from gastown/witness: \"NUDGE\""

	// First call: no saved state, should block
	state := loadStopState(path)
	if state != nil && state.LastReason == reason {
		t.Fatal("should not match on first call")
	}
	saveStopState(path, &stopState{LastReason: reason})

	// Second call: same reason, should NOT block (dedup)
	state = loadStopState(path)
	if state == nil || state.LastReason != reason {
		t.Fatal("should match on second call — dedup should prevent re-block")
	}

	// Condition changes: different reason, should block again
	newReason := "[gt signal stop] Work slung to you: gt-abc — \"Fix bug\""
	if state.LastReason == newReason {
		t.Fatal("different reason should not match")
	}
	saveStopState(path, &stopState{LastReason: newReason})

	// Condition clears: clear state
	clearStopState(path)

	// Same original reason reappears: should block (state was cleared)
	state = loadStopState(path)
	if state != nil {
		t.Fatal("should have no state after clear — allows re-notification")
	}
}
