package cmd

import (
	"testing"
	"time"
)

// TestApplyQueueMessageLabels verifies that applyQueueMessageLabels correctly
// populates From, ClaimedBy, and ClaimedAt from beads labels.
func TestApplyQueueMessageLabels(t *testing.T) {
	rfc := "2026-04-29T12:34:56Z"
	parsed, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatalf("parse reference time: %v", err)
	}

	tests := []struct {
		name          string
		labels        []string
		wantFrom      string
		wantClaimedBy string
		wantClaimedAt *time.Time
	}{
		{
			name:   "empty labels",
			labels: []string{},
		},
		{
			name:     "from label only",
			labels:   []string{"from:mayor/"},
			wantFrom: "mayor/",
		},
		{
			name:          "from and claimed-by",
			labels:        []string{"from:mayor/", "claimed-by:gastown/polecats/opal"},
			wantFrom:      "mayor/",
			wantClaimedBy: "gastown/polecats/opal",
		},
		{
			name: "all three labels",
			labels: []string{
				"from:gastown/witness",
				"claimed-by:gastown/polecats/opal",
				"claimed-at:" + rfc,
			},
			wantFrom:      "gastown/witness",
			wantClaimedBy: "gastown/polecats/opal",
			wantClaimedAt: &parsed,
		},
		{
			name: "malformed claimed-at leaves nil",
			labels: []string{
				"from:mayor/",
				"claimed-at:not-a-timestamp",
			},
			wantFrom:      "mayor/",
			wantClaimedAt: nil,
		},
		{
			name: "unknown labels ignored",
			labels: []string{
				"priority:urgent",
				"queue:work",
				"gt:message",
				"from:mayor/",
			},
			wantFrom: "mayor/",
		},
		{
			name: "duplicate from labels — last wins",
			labels: []string{
				"from:mayor/",
				"from:gastown/witness",
			},
			wantFrom: "gastown/witness",
		},
		{
			name: "claimed-by with empty value",
			labels: []string{
				"claimed-by:",
			},
			wantClaimedBy: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg queueMessage
			applyQueueMessageLabels(&msg, tt.labels)

			if msg.From != tt.wantFrom {
				t.Errorf("From = %q, want %q", msg.From, tt.wantFrom)
			}
			if msg.ClaimedBy != tt.wantClaimedBy {
				t.Errorf("ClaimedBy = %q, want %q", msg.ClaimedBy, tt.wantClaimedBy)
			}
			switch {
			case tt.wantClaimedAt == nil && msg.ClaimedAt != nil:
				t.Errorf("ClaimedAt = %v, want nil", msg.ClaimedAt)
			case tt.wantClaimedAt != nil && msg.ClaimedAt == nil:
				t.Errorf("ClaimedAt = nil, want %v", tt.wantClaimedAt)
			case tt.wantClaimedAt != nil && !msg.ClaimedAt.Equal(*tt.wantClaimedAt):
				t.Errorf("ClaimedAt = %v, want %v", msg.ClaimedAt, tt.wantClaimedAt)
			}
		})
	}
}

// TestApplyQueueMessageInfoLabels verifies that applyQueueMessageInfoLabels
// populates QueueName, ClaimedBy, and ClaimedAt for a queueMessageInfo.
func TestApplyQueueMessageInfoLabels(t *testing.T) {
	rfc := "2026-04-29T09:15:30Z"
	parsed, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatalf("parse reference time: %v", err)
	}

	tests := []struct {
		name          string
		labels        []string
		wantQueue     string
		wantClaimedBy string
		wantClaimedAt *time.Time
	}{
		{
			name:   "empty labels",
			labels: []string{},
		},
		{
			name:      "queue label only",
			labels:    []string{"queue:work-requests"},
			wantQueue: "work-requests",
		},
		{
			name: "fully claimed message",
			labels: []string{
				"queue:work-requests",
				"claimed-by:gastown/polecats/opal",
				"claimed-at:" + rfc,
				"gt:message",
			},
			wantQueue:     "work-requests",
			wantClaimedBy: "gastown/polecats/opal",
			wantClaimedAt: &parsed,
		},
		{
			name: "malformed claimed-at is ignored",
			labels: []string{
				"queue:work",
				"claimed-by:gastown/polecats/opal",
				"claimed-at:garbage",
			},
			wantQueue:     "work",
			wantClaimedBy: "gastown/polecats/opal",
			wantClaimedAt: nil,
		},
		{
			name: "non-queue labels ignored",
			labels: []string{
				"from:mayor/",
				"priority:high",
				"queue:dispatch",
			},
			wantQueue: "dispatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &queueMessageInfo{}
			applyQueueMessageInfoLabels(info, tt.labels)

			if info.QueueName != tt.wantQueue {
				t.Errorf("QueueName = %q, want %q", info.QueueName, tt.wantQueue)
			}
			if info.ClaimedBy != tt.wantClaimedBy {
				t.Errorf("ClaimedBy = %q, want %q", info.ClaimedBy, tt.wantClaimedBy)
			}
			switch {
			case tt.wantClaimedAt == nil && info.ClaimedAt != nil:
				t.Errorf("ClaimedAt = %v, want nil", info.ClaimedAt)
			case tt.wantClaimedAt != nil && info.ClaimedAt == nil:
				t.Errorf("ClaimedAt = nil, want %v", tt.wantClaimedAt)
			case tt.wantClaimedAt != nil && !info.ClaimedAt.Equal(*tt.wantClaimedAt):
				t.Errorf("ClaimedAt = %v, want %v", info.ClaimedAt, tt.wantClaimedAt)
			}
		})
	}
}

// TestApplyQueueMessageInfoLabels_PreservesExistingID verifies that label
// application does not clobber fields set by the caller (ID, Title, Status).
// This guards against regressions where label handling accidentally overwrites
// the struct fields populated from the beads issue.
func TestApplyQueueMessageInfoLabels_PreservesExistingID(t *testing.T) {
	info := &queueMessageInfo{
		ID:     "hq-test-preserved",
		Title:  "Preserved title",
		Status: "open",
	}
	applyQueueMessageInfoLabels(info, []string{"queue:work", "claimed-by:alice"})

	if info.ID != "hq-test-preserved" {
		t.Errorf("ID clobbered: got %q", info.ID)
	}
	if info.Title != "Preserved title" {
		t.Errorf("Title clobbered: got %q", info.Title)
	}
	if info.Status != "open" {
		t.Errorf("Status clobbered: got %q", info.Status)
	}
	if info.QueueName != "work" {
		t.Errorf("QueueName = %q, want %q", info.QueueName, "work")
	}
	if info.ClaimedBy != "alice" {
		t.Errorf("ClaimedBy = %q, want %q", info.ClaimedBy, "alice")
	}
}

// TestApplyQueueMessageLabels_PreservesExistingFields verifies that label
// application does not clobber ID, Title, Description, Created, or Priority.
func TestApplyQueueMessageLabels_PreservesExistingFields(t *testing.T) {
	created := time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)
	msg := queueMessage{
		ID:          "hq-msg-preserved",
		Title:       "Do the thing",
		Description: "Details about the thing",
		Created:     created,
		Priority:    2,
	}
	applyQueueMessageLabels(&msg, []string{"from:mayor/"})

	if msg.ID != "hq-msg-preserved" {
		t.Errorf("ID clobbered: got %q", msg.ID)
	}
	if msg.Title != "Do the thing" {
		t.Errorf("Title clobbered: got %q", msg.Title)
	}
	if msg.Description != "Details about the thing" {
		t.Errorf("Description clobbered: got %q", msg.Description)
	}
	if !msg.Created.Equal(created) {
		t.Errorf("Created clobbered: got %v", msg.Created)
	}
	if msg.Priority != 2 {
		t.Errorf("Priority clobbered: got %d", msg.Priority)
	}
	if msg.From != "mayor/" {
		t.Errorf("From = %q, want %q", msg.From, "mayor/")
	}
}
