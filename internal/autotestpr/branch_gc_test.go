package autotestpr

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestClassifyBranches_SkipCondition1_Young verifies that branches with
// tip commits younger than the stale threshold are kept.
func TestClassifyBranches_SkipCondition1_Young(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	runner := &BranchGCRunner{
		Config: BranchGCConfig{
			StaleDays:     7,
			BranchPrefix:  "auto-test/",
			AutoTestLabel: "gt:auto-test-pr",
			Now:           now,
		},
		Beads: nil, // No beads needed for age check
	}

	candidates := []BranchCandidate{
		{
			Rig:       "gastown_upstream",
			Ref:       "auto-test/gastown_upstream/gu-fresh",
			BeadID:    "gu-fresh",
			Timestamp: now.Add(-2 * 24 * time.Hour).Unix(), // 2 days old
		},
		{
			Rig:       "gastown_upstream",
			Ref:       "auto-test/gastown_upstream/gu-borderline",
			BeadID:    "gu-borderline",
			Timestamp: now.Add(-6 * 24 * time.Hour).Unix(), // 6 days old (within 7d)
		},
	}

	results := runner.ClassifyBranches(candidates)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if !r.Keep {
			t.Errorf("branch %s should be kept (young), got delete", r.Ref)
		}
	}
}

// TestClassifyBranches_SkipCondition1_Stale verifies that branches
// older than the threshold (with no other skip condition) are marked
// for deletion.
func TestClassifyBranches_SkipCondition1_Stale(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	runner := &BranchGCRunner{
		Config: BranchGCConfig{
			StaleDays:     7,
			BranchPrefix:  "auto-test/",
			AutoTestLabel: "gt:auto-test-pr",
			Now:           now,
		},
		Beads: nil, // No beads — skip conditions 2-4 won't fire
	}

	candidates := []BranchCandidate{
		{
			Rig:       "gastown_upstream",
			Ref:       "auto-test/gastown_upstream/gu-old",
			BeadID:    "gu-old",
			Timestamp: now.Add(-10 * 24 * time.Hour).Unix(), // 10 days old
		},
		{
			Rig:       "gastown_upstream",
			Ref:       "auto-test/gastown_upstream/gu-ancient",
			BeadID:    "gu-ancient",
			Timestamp: now.Add(-30 * 24 * time.Hour).Unix(), // 30 days old
		},
	}

	results := runner.ClassifyBranches(candidates)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Keep {
			t.Errorf("branch %s should be marked for deletion (stale), got keep: %s", r.Ref, r.Reason)
		}
	}
}

// TestClassifyBranches_MixedAges verifies correct classification of a
// mix of young and stale branches.
func TestClassifyBranches_MixedAges(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	runner := &BranchGCRunner{
		Config: BranchGCConfig{
			StaleDays:     7,
			BranchPrefix:  "auto-test/",
			AutoTestLabel: "gt:auto-test-pr",
			Now:           now,
		},
		Beads: nil,
	}

	candidates := []BranchCandidate{
		{
			Rig:       "gastown_upstream",
			Ref:       "auto-test/gastown_upstream/gu-young",
			BeadID:    "gu-young",
			Timestamp: now.Add(-1 * 24 * time.Hour).Unix(), // 1 day
		},
		{
			Rig:       "gastown_upstream",
			Ref:       "auto-test/gastown_upstream/gu-stale",
			BeadID:    "gu-stale",
			Timestamp: now.Add(-14 * 24 * time.Hour).Unix(), // 14 days
		},
	}

	results := runner.ClassifyBranches(candidates)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First should be kept (young)
	if !results[0].Keep {
		t.Errorf("young branch should be kept, got delete: %s", results[0].Reason)
	}
	// Second should be deleted (stale)
	if results[1].Keep {
		t.Errorf("stale branch should be deleted, got keep: %s", results[1].Reason)
	}
}

// TestDeleteStaleBranches_DryRun verifies that dry-run mode reports
// candidates without attempting deletion.
func TestDeleteStaleBranches_DryRun(t *testing.T) {
	runner := &BranchGCRunner{
		Config: BranchGCConfig{
			DryRun:             true,
			MaxDeletesPerCycle: 50,
		},
	}

	classified := []ClassifiedBranch{
		{BranchCandidate: BranchCandidate{Rig: "gastown_upstream", Ref: "auto-test/gastown_upstream/gu-del1"}, Keep: false, Reason: "10d"},
		{BranchCandidate: BranchCandidate{Rig: "gastown_upstream", Ref: "auto-test/gastown_upstream/gu-del2"}, Keep: false, Reason: "15d"},
		{BranchCandidate: BranchCandidate{Rig: "gastown_upstream", Ref: "auto-test/gastown_upstream/gu-keep"}, Keep: true, Reason: "young"},
	}

	result := runner.DeleteStaleBranches(classified, nil)

	if len(result.Kept) != 1 {
		t.Errorf("expected 1 kept, got %d", len(result.Kept))
	}
	if len(result.Deleted) != 2 {
		t.Errorf("expected 2 deleted, got %d", len(result.Deleted))
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors in dry-run, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestDefaultBranchGCConfig verifies defaults match the formula vars.
func TestDefaultBranchGCConfig(t *testing.T) {
	cfg := DefaultBranchGCConfig()

	if !cfg.DryRun {
		t.Error("DryRun should default to true")
	}
	if cfg.StaleDays != 7 {
		t.Errorf("StaleDays = %d, want 7", cfg.StaleDays)
	}
	if cfg.BranchPrefix != "auto-test/" {
		t.Errorf("BranchPrefix = %q, want %q", cfg.BranchPrefix, "auto-test/")
	}
	if cfg.AutoTestLabel != "gt:auto-test-pr" {
		t.Errorf("AutoTestLabel = %q, want %q", cfg.AutoTestLabel, "gt:auto-test-pr")
	}
	if cfg.MaxDeletesPerCycle != 50 {
		t.Errorf("MaxDeletesPerCycle = %d, want 50", cfg.MaxDeletesPerCycle)
	}
}

// TestIsActiveStatus verifies the status classification used by
// skip condition 3.
func TestIsActiveStatus(t *testing.T) {
	active := []string{"open", "in_progress", "hooked", "blocked"}
	for _, s := range active {
		if !isActiveStatus(s) {
			t.Errorf("isActiveStatus(%q) = false, want true", s)
		}
	}

	inactive := []string{"closed", "deferred", "pinned", ""}
	for _, s := range inactive {
		if isActiveStatus(s) {
			t.Errorf("isActiveStatus(%q) = true, want false", s)
		}
	}
}

// --- Attachment-Bead Retention Tests ---

// TestAttachmentRetention_TransitionWithinWindow verifies that
// transition beads within the 60d window are kept open.
func TestAttachmentRetention_TransitionWithinWindow(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	runner := &AttachmentRetentionRunner{
		Config: AttachmentRetentionConfig{
			TransitionRetentionDays: 60,
			RejectionRetentionDays:  30,
			AttachmentLabel:         "gt:auto-test-pr-attachment",
			Now:                     now,
		},
		DryRun: true,
	}

	// Transition bead created 30 days ago — within 60d window
	meta := map[string]interface{}{
		"schema_version": 1,
		"at":             now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	iss := makeTestIssue("gu-att-recent",
		[]string{"gt:auto-test-pr-attachment", "kind:transition", "rig:gastown_upstream"},
		metaJSON)

	closure := runner.evaluateAttachment(iss, now)
	if closure != nil {
		t.Errorf("expected nil (within retention), got closure: %+v", closure)
	}
}

// TestAttachmentRetention_TransitionOutsideWindow verifies that
// transition beads older than 60d are marked for closure.
func TestAttachmentRetention_TransitionOutsideWindow(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	runner := &AttachmentRetentionRunner{
		Config: AttachmentRetentionConfig{
			TransitionRetentionDays: 60,
			RejectionRetentionDays:  30,
			AttachmentLabel:         "gt:auto-test-pr-attachment",
			Now:                     now,
		},
		DryRun: true,
	}

	// Transition bead created 90 days ago — outside 60d window
	meta := map[string]interface{}{
		"schema_version": 1,
		"at":             now.Add(-90 * 24 * time.Hour).Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	iss := makeTestIssue("gu-att-old",
		[]string{"gt:auto-test-pr-attachment", "kind:transition", "rig:gastown_upstream"},
		metaJSON)

	closure := runner.evaluateAttachment(iss, now)
	if closure == nil {
		t.Fatal("expected closure (outside retention), got nil")
	}
	if closure.Kind != "transition" {
		t.Errorf("closure.Kind = %q, want %q", closure.Kind, "transition")
	}
	if closure.BeadID != "gu-att-old" {
		t.Errorf("closure.BeadID = %q, want %q", closure.BeadID, "gu-att-old")
	}
}

// TestAttachmentRetention_RejectionWithinCooldown verifies that
// rejection beads within cooldown_until + 30d are kept open.
func TestAttachmentRetention_RejectionWithinCooldown(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	runner := &AttachmentRetentionRunner{
		Config: AttachmentRetentionConfig{
			TransitionRetentionDays: 60,
			RejectionRetentionDays:  30,
			AttachmentLabel:         "gt:auto-test-pr-attachment",
			Now:                     now,
		},
		DryRun: true,
	}

	// Rejection with cooldown_until = 21 days in the future
	// cooldown_until + 30d = 51 days from now — way in the future
	meta := map[string]interface{}{
		"schema_version": 1,
		"cooldown_until": now.Add(21 * 24 * time.Hour).Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	iss := makeTestIssue("gu-rej-active",
		[]string{"gt:auto-test-pr-attachment", "kind:rejection", "rig:gastown_upstream"},
		metaJSON)

	closure := runner.evaluateAttachment(iss, now)
	if closure != nil {
		t.Errorf("expected nil (within cooldown + retention), got closure: %+v", closure)
	}
}

// TestAttachmentRetention_RejectionPastCooldown verifies that
// rejection beads with cooldown_until + 30d < now are marked for closure.
func TestAttachmentRetention_RejectionPastCooldown(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	runner := &AttachmentRetentionRunner{
		Config: AttachmentRetentionConfig{
			TransitionRetentionDays: 60,
			RejectionRetentionDays:  30,
			AttachmentLabel:         "gt:auto-test-pr-attachment",
			Now:                     now,
		},
		DryRun: true,
	}

	// Rejection with cooldown_until = 45 days ago
	// cooldown_until + 30d = 15 days ago → should be closed
	meta := map[string]interface{}{
		"schema_version": 1,
		"cooldown_until": now.Add(-45 * 24 * time.Hour).Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	iss := makeTestIssue("gu-rej-expired",
		[]string{"gt:auto-test-pr-attachment", "kind:rejection", "rig:gastown_upstream"},
		metaJSON)

	closure := runner.evaluateAttachment(iss, now)
	if closure == nil {
		t.Fatal("expected closure (past cooldown + retention), got nil")
	}
	if closure.Kind != "rejection" {
		t.Errorf("closure.Kind = %q, want %q", closure.Kind, "rejection")
	}
	if closure.BeadID != "gu-rej-expired" {
		t.Errorf("closure.BeadID = %q, want %q", closure.BeadID, "gu-rej-expired")
	}
}

// TestAttachmentRetention_AcceptanceCriteria runs the exact scenario
// from the bead acceptance criteria: seed 3 transitions (fresh, 30d,
// 90d) and 3 rejections (cooldowns at +21d, -10d, -45d), verify the
// correct ones are marked for closure.
func TestAttachmentRetention_AcceptanceCriteria(t *testing.T) {
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)

	runner := &AttachmentRetentionRunner{
		Config: AttachmentRetentionConfig{
			TransitionRetentionDays: 60,
			RejectionRetentionDays:  30,
			AttachmentLabel:         "gt:auto-test-pr-attachment",
			Now:                     now,
		},
		DryRun: true,
	}

	// Transitions
	type testCase struct {
		id       string
		labels   []string
		meta     map[string]interface{}
		wantKeep bool
	}

	cases := []testCase{
		// Transition: fresh (5d old) → keep
		{
			id:     "gu-tx-fresh",
			labels: []string{"gt:auto-test-pr-attachment", "kind:transition", "rig:gastown_upstream"},
			meta: map[string]interface{}{
				"schema_version": 1,
				"at":             now.Add(-5 * 24 * time.Hour).Format(time.RFC3339),
			},
			wantKeep: true,
		},
		// Transition: 30d old → keep (within 60d)
		{
			id:     "gu-tx-30d",
			labels: []string{"gt:auto-test-pr-attachment", "kind:transition", "rig:gastown_upstream"},
			meta: map[string]interface{}{
				"schema_version": 1,
				"at":             now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
			},
			wantKeep: true,
		},
		// Transition: 90d old → CLOSE
		{
			id:     "gu-tx-90d",
			labels: []string{"gt:auto-test-pr-attachment", "kind:transition", "rig:gastown_upstream"},
			meta: map[string]interface{}{
				"schema_version": 1,
				"at":             now.Add(-90 * 24 * time.Hour).Format(time.RFC3339),
			},
			wantKeep: false,
		},
		// Rejection: cooldown_until = +21d from now → keep
		{
			id:     "gu-rj-future21",
			labels: []string{"gt:auto-test-pr-attachment", "kind:rejection", "rig:gastown_upstream"},
			meta: map[string]interface{}{
				"schema_version": 1,
				"cooldown_until": now.Add(21 * 24 * time.Hour).Format(time.RFC3339),
			},
			wantKeep: true,
		},
		// Rejection: cooldown_until = -10d → cooldown+30d = +20d from now → keep
		{
			id:     "gu-rj-past10",
			labels: []string{"gt:auto-test-pr-attachment", "kind:rejection", "rig:gastown_upstream"},
			meta: map[string]interface{}{
				"schema_version": 1,
				"cooldown_until": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
			},
			wantKeep: true,
		},
		// Rejection: cooldown_until = -45d → cooldown+30d = -15d → CLOSE
		{
			id:     "gu-rj-past45",
			labels: []string{"gt:auto-test-pr-attachment", "kind:rejection", "rig:gastown_upstream"},
			meta: map[string]interface{}{
				"schema_version": 1,
				"cooldown_until": now.Add(-45 * 24 * time.Hour).Format(time.RFC3339),
			},
			wantKeep: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			metaJSON, _ := json.Marshal(tc.meta)
			iss := makeTestIssue(tc.id, tc.labels, metaJSON)

			closure := runner.evaluateAttachment(iss, now)
			if tc.wantKeep && closure != nil {
				t.Errorf("expected keep, got closure: %+v", closure)
			}
			if !tc.wantKeep && closure == nil {
				t.Error("expected closure, got nil (keep)")
			}
		})
	}
}

// TestDefaultAttachmentRetentionConfig verifies defaults match the design doc.
func TestDefaultAttachmentRetentionConfig(t *testing.T) {
	cfg := DefaultAttachmentRetentionConfig()

	if cfg.TransitionRetentionDays != 60 {
		t.Errorf("TransitionRetentionDays = %d, want 60", cfg.TransitionRetentionDays)
	}
	if cfg.RejectionRetentionDays != 30 {
		t.Errorf("RejectionRetentionDays = %d, want 30", cfg.RejectionRetentionDays)
	}
	if cfg.AttachmentLabel != "gt:auto-test-pr-attachment" {
		t.Errorf("AttachmentLabel = %q, want %q", cfg.AttachmentLabel, "gt:auto-test-pr-attachment")
	}
}

// TestParseBeadTime verifies the time parsing helper handles various formats.
func TestParseBeadTime(t *testing.T) {
	tests := []struct {
		input string
		want  string // expected RFC3339 output or empty for zero
	}{
		{"2026-05-26T01:47:35Z", "2026-05-26T01:47:35Z"},
		{"2026-05-26 01:47:35", "2026-05-26T01:47:35Z"},
		{"2026-05-26", "2026-05-26T00:00:00Z"},
		{"", ""},
		{"garbage", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseBeadTime(tt.input)
			if tt.want == "" {
				if !result.IsZero() {
					t.Errorf("parseBeadTime(%q) = %v, want zero", tt.input, result)
				}
			} else {
				expected, _ := time.Parse(time.RFC3339, tt.want)
				if !result.Equal(expected) {
					t.Errorf("parseBeadTime(%q) = %v, want %v", tt.input, result, expected)
				}
			}
		})
	}
}

// --- Test Helpers ---

// makeTestIssue constructs a *beads.Issue for unit testing the
// retention evaluation methods.
func makeTestIssue(id string, labels []string, metadata json.RawMessage) *beads.Issue {
	return &beads.Issue{
		ID:       id,
		Labels:   labels,
		Metadata: metadata,
		Status:   "open",
	}
}
