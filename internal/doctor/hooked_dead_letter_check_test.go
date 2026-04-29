package doctor

import (
	"strings"
	"testing"
)

func TestNewHookedDeadLetterCheck(t *testing.T) {
	check := NewHookedDeadLetterCheck()
	if check == nil {
		t.Fatal("NewHookedDeadLetterCheck returned nil")
	}
	if check.Name() != "hooked-dead-letter" {
		t.Errorf("Name() = %q, want %q", check.Name(), "hooked-dead-letter")
	}
	if check.Category() != CategoryCleanup {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryCleanup)
	}
	if check.CanFix() {
		t.Errorf("CanFix() = true, want false (check is report-only; fix is via 'gt reaper reap-hooked-mail')")
	}
	if check.threshold <= 0 {
		t.Errorf("threshold = %d, want positive default", check.threshold)
	}
}

func TestDefaultDeadLetterThreshold(t *testing.T) {
	if DefaultDeadLetterThreshold <= 0 {
		t.Errorf("DefaultDeadLetterThreshold should be positive, got %d", DefaultDeadLetterThreshold)
	}
	// gu-hhqk AC #4 explicitly sets this to 10. Regression guard.
	if DefaultDeadLetterThreshold != 10 {
		t.Errorf("DefaultDeadLetterThreshold = %d, want 10 per gu-hhqk AC #4", DefaultDeadLetterThreshold)
	}
}

func TestHookedDeadLetterCountQueryStructure(t *testing.T) {
	// The SQL must filter on the right label and exclude agent beads +
	// long-lived conventional labels, matching the reaper's exclusions.
	wantSubstrings := []string{
		"status = 'hooked'",
		"'agent'",
		"'gt:message'",
		"INTERVAL 30 MINUTE",
		"gt:standing-orders",
		"gt:keep",
		"gt:role",
		"gt:rig",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(hookedDeadLetterCountQuery, want) {
			t.Errorf("query missing expected substring %q:\n%s", want, hookedDeadLetterCountQuery)
		}
	}
}

func TestHookedDeadLetterCheck_Run_NoDatabases(t *testing.T) {
	// When ListDatabases returns nothing (no Dolt or no rigs), the check should
	// return OK with a skip message — not panic or error.
	check := NewHookedDeadLetterCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)
	if result == nil {
		t.Fatal("Run returned nil")
	}
	if result.Status != StatusOK {
		t.Errorf("Run with no databases: status = %v, want StatusOK", result.Status)
	}
	if !strings.Contains(result.Message, "No rig databases") {
		t.Errorf("Run with no databases: message = %q, want to mention missing databases", result.Message)
	}
}
