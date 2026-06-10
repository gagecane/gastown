package reaper

import (
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
