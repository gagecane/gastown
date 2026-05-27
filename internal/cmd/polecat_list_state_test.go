package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

type fakeReuseMRShower struct {
	issue *beads.Issue
	err   error
}

func (f fakeReuseMRShower) Show(issueID string) (*beads.Issue, error) {
	return f.issue, f.err
}

func TestEffectivePolecatState(t *testing.T) {
	tests := []struct {
		name string
		item PolecatListItem
		want polecat.State
	}{
		{
			name: "session-running-done-becomes-working",
			item: PolecatListItem{
				State:          polecat.StateDone,
				SessionRunning: true,
			},
			want: polecat.StateWorking,
		},
		{
			name: "session-dead-working-becomes-stalled",
			item: PolecatListItem{
				State:          polecat.StateWorking,
				SessionRunning: false,
			},
			want: polecat.StateStalled,
		},
		{
			name: "zombie-is-never-rewritten",
			item: PolecatListItem{
				State:          polecat.StateZombie,
				SessionRunning: false,
				Zombie:         true,
			},
			want: polecat.StateZombie,
		},
		{
			name: "idle-session-dead-stays-idle",
			item: PolecatListItem{
				State:          polecat.StateIdle,
				SessionRunning: false,
			},
			want: polecat.StateIdle,
		},
		{
			name: "idle-session-running-becomes-working",
			item: PolecatListItem{
				State:          polecat.StateIdle,
				SessionRunning: true,
			},
			want: polecat.StateWorking,
		},
		{
			name: "stalled-stays-stalled-when-session-dead",
			item: PolecatListItem{
				State:          polecat.StateStalled,
				SessionRunning: false,
			},
			want: polecat.StateStalled,
		},
		{
			name: "stalled-becomes-working-when-session-alive",
			item: PolecatListItem{
				State:          polecat.StateStalled,
				SessionRunning: true,
			},
			want: polecat.StateStalled, // stalled is a detected state, session running doesn't override
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectivePolecatState(tt.item)
			if got != tt.want {
				t.Fatalf("effectivePolecatState() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSessionLabel ensures the plain-text session label is emitted alongside
// the ●/○ glyph so operators can tell live from dead sessions even when the
// output is piped, logged, or rendered by a terminal that strips color/unicode.
// Before this was added, the default `gt polecat list` output was visually
// ambiguous: an "idle" polecat with a reaped session and a "working" polecat
// whose session had just died both showed an ○ glyph that could be lost.
func TestSessionLabel(t *testing.T) {
	tests := []struct {
		name string
		item PolecatListItem
		want string
	}{
		{
			name: "alive-session",
			item: PolecatListItem{SessionRunning: true},
			want: "session: alive",
		},
		{
			name: "dead-session",
			item: PolecatListItem{SessionRunning: false},
			want: "session: dead",
		},
		{
			name: "idle-with-no-session-is-labeled-dead",
			item: PolecatListItem{
				State:          polecat.StateIdle,
				SessionRunning: false,
			},
			want: "session: dead",
		},
		{
			name: "zombie-has-live-session",
			item: PolecatListItem{
				State:          polecat.StateZombie,
				SessionRunning: true,
				Zombie:         true,
			},
			want: "session: alive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionLabel(tt.item)
			if got != tt.want {
				t.Fatalf("sessionLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActiveMRBlocksReuse(t *testing.T) {
	tests := []struct {
		name string
		mrID string
		bd   reuseMRShower
		want bool
	}{
		{name: "empty active MR does not block"},
		{
			name: "open MR blocks reuse",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}},
			want: true,
		},
		{
			name: "closed MR does not block reuse",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{issue: &beads.Issue{ID: "mr-1", Status: "closed"}},
			want: false,
		},
		{
			name: "lookup error blocks conservatively",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{err: errors.New("bd exploded")},
			want: true,
		},
		{
			name: "missing MR blocks conservatively",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeMRBlocksReuse(tt.bd, tt.mrID); got != tt.want {
				t.Fatalf("activeMRBlocksReuse() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFilterDeadPolecats verifies the --dead-only filter keeps only polecats
// with no live tmux session. This includes both legitimately idle polecats
// (session was reaped after gt done) and stalled polecats (session died
// mid-work). Zombies — which have a live session but no worktree — must be
// excluded so the operator can focus on "where did my agents go" instead of
// "what leftover sessions need cleanup".
func TestFilterDeadPolecats(t *testing.T) {
	input := []PolecatListItem{
		{Rig: "a", Name: "alpha", State: polecat.StateWorking, SessionRunning: true},
		{Rig: "a", Name: "bravo", State: polecat.StateIdle, SessionRunning: false},
		{Rig: "a", Name: "charlie", State: polecat.StateStalled, SessionRunning: false},
		{Rig: "b", Name: "delta", State: polecat.StateZombie, SessionRunning: true, Zombie: true},
		{Rig: "b", Name: "echo", State: polecat.StateDone, SessionRunning: true},
	}

	got := filterDeadPolecats(input)

	if len(got) != 2 {
		t.Fatalf("filterDeadPolecats returned %d items, want 2; got=%v", len(got), got)
	}

	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
		if p.SessionRunning {
			t.Errorf("filterDeadPolecats kept item with SessionRunning=true: %+v", p)
		}
	}

	for _, want := range []string{"bravo", "charlie"} {
		if !names[want] {
			t.Errorf("filterDeadPolecats missing expected dead polecat %q; got names=%v", want, names)
		}
	}
	for _, unwanted := range []string{"alpha", "delta", "echo"} {
		if names[unwanted] {
			t.Errorf("filterDeadPolecats unexpectedly kept live-session polecat %q", unwanted)
		}
	}
}

// TestFilterDeadPolecatsEmpty ensures the filter handles an empty input
// slice without panicking and without returning nil (callers iterate over
// the result without a nil check).
func TestFilterDeadPolecatsEmpty(t *testing.T) {
	got := filterDeadPolecats(nil)
	if got == nil {
		t.Fatalf("filterDeadPolecats(nil) = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("filterDeadPolecats(nil) returned %d items, want 0", len(got))
	}
}

// TestFilterDeadPolecatsAllDead verifies the filter preserves order when
// every input is dead — a common case on machines where the tmux server
// restarted and killed all polecat sessions at once.
func TestFilterDeadPolecatsAllDead(t *testing.T) {
	input := []PolecatListItem{
		{Name: "first", SessionRunning: false},
		{Name: "second", SessionRunning: false},
		{Name: "third", SessionRunning: false},
	}
	got := filterDeadPolecats(input)
	if len(got) != len(input) {
		t.Fatalf("filterDeadPolecats dropped items; got %d, want %d", len(got), len(input))
	}
	for i, p := range got {
		if p.Name != input[i].Name {
			t.Errorf("index %d: got name=%q, want %q", i, p.Name, input[i].Name)
		}
	}
}

func TestPolecatReuseStatus(t *testing.T) {
	tests := []struct {
		name           string
		state          polecat.State
		cleanupStatus  string
		activeMR       string
		branch         string
		activeMRBlocks bool
		want           string
	}{
		{
			name:  "working has no reuse status",
			state: polecat.StateWorking,
			want:  "",
		},
		{
			name:          "idle missing cleanup is recovery needed",
			state:         polecat.StateIdle,
			cleanupStatus: "",
			want:          "idle-recovery-needed",
		},
		{
			name:          "idle dirty cleanup is recovery needed",
			state:         polecat.StateIdle,
			cleanupStatus: string(polecat.CleanupUnpushed),
			want:          "idle-recovery-needed",
		},
		{
			name:           "idle open MR is pr open",
			state:          polecat.StateIdle,
			cleanupStatus:  string(polecat.CleanupClean),
			activeMR:       "mr-1",
			activeMRBlocks: true,
			want:           "idle-pr-open",
		},
		{
			name:          "idle clean old branch is preserved",
			state:         polecat.StateIdle,
			cleanupStatus: string(polecat.CleanupClean),
			branch:        "polecat/chrome/old-work",
			want:          "idle-preserved",
		},
		{
			name:          "idle clean main is clean",
			state:         polecat.StateIdle,
			cleanupStatus: string(polecat.CleanupClean),
			branch:        "main",
			want:          "idle-clean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := polecatReuseStatus(tt.state, tt.cleanupStatus, tt.activeMR, tt.branch, tt.activeMRBlocks)
			if got != tt.want {
				t.Fatalf("polecatReuseStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
