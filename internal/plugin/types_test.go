package plugin

import (
	"testing"
	"time"
)

func TestStuckThreshold(t *testing.T) {
	const blanket = 2 * time.Hour

	tests := []struct {
		name   string
		plugin *Plugin
		want   time.Duration
	}{
		{
			name:   "nil gate falls back to blanket",
			plugin: &Plugin{Name: "no-gate"},
			want:   blanket,
		},
		{
			name:   "non-cooldown gate falls back to blanket",
			plugin: &Plugin{Name: "cron", Gate: &Gate{Type: GateCron, Schedule: "0 9 * * *"}},
			want:   blanket,
		},
		{
			name:   "empty duration falls back to blanket",
			plugin: &Plugin{Name: "bad", Gate: &Gate{Type: GateCooldown}},
			want:   blanket,
		},
		{
			name:   "unparseable duration falls back to blanket",
			plugin: &Plugin{Name: "bad", Gate: &Gate{Type: GateCooldown, Duration: "notaduration"}},
			want:   blanket,
		},
		{
			name: "dolt-backup 15m cooldown -> 2x interval = 30m",
			plugin: &Plugin{
				Name:      "dolt-backup",
				Gate:      &Gate{Type: GateCooldown, Duration: "15m"},
				Execution: &Execution{Timeout: "5m"},
			},
			want: 30 * time.Minute,
		},
		{
			name: "compactor 30m cooldown -> 2x interval = 1h",
			plugin: &Plugin{
				Name:      "compactor-dog",
				Gate:      &Gate{Type: GateCooldown, Duration: "30m"},
				Execution: &Execution{Timeout: "5m"},
			},
			want: time.Hour,
		},
		{
			name: "long execution timeout floors above 2x cooldown",
			plugin: &Plugin{
				Name:      "ci-watcher-poll",
				Gate:      &Gate{Type: GateCooldown, Duration: "3m"},
				Execution: &Execution{Timeout: "5m"},
			},
			// 2x3m=6m, but exec floor 5m+2m=7m wins.
			want: 7 * time.Minute,
		},
		{
			name: "long cooldown clamps to blanket",
			plugin: &Plugin{
				Name: "git-hygiene",
				Gate: &Gate{Type: GateCooldown, Duration: "12h"},
			},
			// 2x12h=24h, clamped to 2h blanket.
			want: blanket,
		},
		{
			name: "no execution block uses interval only",
			plugin: &Plugin{
				Name: "auto-dispatch",
				Gate: &Gate{Type: GateCooldown, Duration: "2m"},
			},
			want: 4 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.plugin.StuckThreshold(blanket)
			if got != tt.want {
				t.Errorf("StuckThreshold(%v) = %v, want %v", blanket, got, tt.want)
			}
		})
	}
}

// TestStuckThreshold_NonPositiveBlanket ensures a zero/negative blanket disables
// the upper clamp rather than forcing every plugin to zero.
func TestStuckThreshold_NonPositiveBlanket(t *testing.T) {
	p := &Plugin{
		Name: "git-hygiene",
		Gate: &Gate{Type: GateCooldown, Duration: "12h"},
	}
	got := p.StuckThreshold(0)
	want := 24 * time.Hour // 2x12h, no clamp applied
	if got != want {
		t.Errorf("StuckThreshold(0) = %v, want %v", got, want)
	}
}
