package reaper

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateOpenWispAlert(t *testing.T) {
	tests := []struct {
		name         string
		open         int
		threshold    int
		wantFire     bool
		wantBucket   int
		wantSeverity string
		wantCooldown time.Duration
		wantSig      string
	}{
		{
			name:      "below threshold does not fire",
			open:      799,
			threshold: 800,
			wantFire:  false,
		},
		{
			name:      "exactly at threshold does not fire",
			open:      800,
			threshold: 800,
			wantFire:  false,
		},
		{
			name:         "band 1 breach is medium with 4h cooldown",
			open:         801,
			threshold:    800,
			wantFire:     true,
			wantBucket:   1,
			wantSeverity: "medium",
			wantCooldown: 4 * time.Hour,
			wantSig:      "reaper:open-wisp-breach:b1",
		},
		{
			name:         "high end of band 1 stays band 1",
			open:         1599,
			threshold:    800,
			wantFire:     true,
			wantBucket:   1,
			wantSeverity: "medium",
			wantCooldown: 4 * time.Hour,
			wantSig:      "reaper:open-wisp-breach:b1",
		},
		{
			name:         "band 2 breach is high with 1h cooldown",
			open:         1600,
			threshold:    800,
			wantFire:     true,
			wantBucket:   2,
			wantSeverity: "high",
			wantCooldown: 1 * time.Hour,
			wantSig:      "reaper:open-wisp-breach:b2",
		},
		{
			name:         "band 3 breach stays high",
			open:         2401,
			threshold:    800,
			wantFire:     true,
			wantBucket:   3,
			wantSeverity: "high",
			wantCooldown: 1 * time.Hour,
			wantSig:      "reaper:open-wisp-breach:b3",
		},
		{
			name:      "zero threshold disables alerting",
			open:      5000,
			threshold: 0,
			wantFire:  false,
		},
		{
			name:      "negative threshold disables alerting",
			open:      5000,
			threshold: -1,
			wantFire:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateOpenWispAlert(tt.open, tt.threshold)
			if got.Fire != tt.wantFire {
				t.Fatalf("Fire = %v, want %v", got.Fire, tt.wantFire)
			}
			if !tt.wantFire {
				return
			}
			if got.Bucket != tt.wantBucket {
				t.Errorf("Bucket = %d, want %d", got.Bucket, tt.wantBucket)
			}
			if got.Severity != tt.wantSeverity {
				t.Errorf("Severity = %q, want %q", got.Severity, tt.wantSeverity)
			}
			if got.Cooldown != tt.wantCooldown {
				t.Errorf("Cooldown = %v, want %v", got.Cooldown, tt.wantCooldown)
			}
			if got.Signature != tt.wantSig {
				t.Errorf("Signature = %q, want %q", got.Signature, tt.wantSig)
			}
		})
	}
}

// TestOpenWispAlertSignatureStableAcrossDrift is the core gu-ka8aj guard: two
// different counts within the same breach band must produce the SAME dedup
// signature so `gt escalate --dedup` collapses them into one escalation.
func TestOpenWispAlertSignatureStableAcrossDrift(t *testing.T) {
	a := EvaluateOpenWispAlert(805, 800)
	b := EvaluateOpenWispAlert(812, 800) // <2% drift, same band
	if a.Signature != b.Signature {
		t.Errorf("signature drifted within band: %q vs %q", a.Signature, b.Signature)
	}
	if a.Severity != b.Severity {
		t.Errorf("severity drifted within band: %q vs %q", a.Severity, b.Severity)
	}
}

func TestEscalateArgsNilWhenNotFiring(t *testing.T) {
	a := EvaluateOpenWispAlert(700, 800)
	if got := a.EscalateArgs(700, 800); got != nil {
		t.Fatalf("EscalateArgs on non-firing alert = %v, want nil", got)
	}
}

// TestEscalateArgsCarriesDedupMetadata verifies the firing alert produces a
// `gt escalate` arg vector wired for close-aware dedup: the bucket signature
// (not the exact count) and the band's cooldown as the dedup window. This is
// what stops the every-cycle re-fire described in gu-ka8aj.
func TestEscalateArgsCarriesDedupMetadata(t *testing.T) {
	a := EvaluateOpenWispAlert(812, 800) // band 1, medium, 4h
	args := a.EscalateArgs(812, 800)
	joined := strings.Join(args, " ")

	wantSubstrings := []string{
		"escalate",
		"-s medium",
		"--dedup",
		"--signature=reaper:open-wisp-breach:b1",
		"--dedup-window=4h0m0s",
		"--source=" + openWispAlertSource,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Errorf("EscalateArgs missing %q\n  got: %s", want, joined)
		}
	}

	// The exact count belongs in the human-readable title/reason, never the
	// dedup signature — otherwise drift defeats dedup.
	for i, arg := range args {
		if arg == "--signature=reaper:open-wisp-breach:b1" {
			continue
		}
		if strings.HasPrefix(arg, "--signature=") {
			t.Errorf("unexpected signature arg at %d: %q", i, arg)
		}
	}
}

func TestEscalateArgsHighBandUsesShortCooldown(t *testing.T) {
	a := EvaluateOpenWispAlert(1700, 800) // band 2, high, 1h
	joined := strings.Join(a.EscalateArgs(1700, 800), " ")
	if !strings.Contains(joined, "-s high") {
		t.Errorf("band 2 should escalate at high severity; got: %s", joined)
	}
	if !strings.Contains(joined, "--dedup-window=1h0m0s") {
		t.Errorf("band 2 should use 1h cooldown window; got: %s", joined)
	}
	if !strings.Contains(joined, "--signature=reaper:open-wisp-breach:b2") {
		t.Errorf("band 2 signature mismatch; got: %s", joined)
	}
}
