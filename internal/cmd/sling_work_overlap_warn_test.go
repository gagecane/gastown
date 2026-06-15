package cmd

import (
	"reflect"
	"testing"
)

func TestTitleJaccard(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		want   float64
		approx bool
	}{
		{"identical after stopword strip", "Fix the scheduler race", "Fix scheduler race", 1.0, false},
		{"disjoint", "scheduler race", "dolt backup sync", 0, false},
		{"empty a", "", "scheduler race", 0, false},
		{"empty b", "scheduler race", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := titleJaccard(titleTokenSet(tc.a), titleTokenSet(tc.b))
			if got != tc.want {
				t.Errorf("titleJaccard(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestTitleTokenSetStripsStopwords(t *testing.T) {
	got := titleTokenSet("Fix the bug in scheduler")
	// "fix", "the", "bug", "in" are all stopwords → only "scheduler" survives.
	want := map[string]bool{"scheduler": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("titleTokenSet = %v, want %v", got, want)
	}
}

func TestDetectOverlappingInFlightWork(t *testing.T) {
	// Models the gu-jaxdl / gs-asme incident: two beads, near-identical titles.
	candidates := []overlapCandidate{
		{ID: "gs-asme", Title: "Suppress duplicate alarm in capacity dispatch"},
		{ID: "gu-other", Title: "Refactor dolt backup sync retry"},
		{ID: "gu-self", Title: "Suppress duplicate alarm in capacity dispatch"},
	}

	tests := []struct {
		name        string
		targetID    string
		targetTitle string
		wantIDs     []string
	}{
		{
			name:        "near-identical in-flight bead is flagged",
			targetID:    "gu-jaxdl",
			targetTitle: "Suppress duplicate alarm in capacity dispatch",
			wantIDs:     []string{"gs-asme", "gu-self"},
		},
		{
			name:        "self is excluded by ID",
			targetID:    "gs-asme",
			targetTitle: "Suppress duplicate alarm in capacity dispatch",
			wantIDs:     []string{"gu-self"},
		},
		{
			name:        "unrelated title does not warn",
			targetID:    "gu-new",
			targetTitle: "Add metrics endpoint to witness daemon",
			wantIDs:     nil,
		},
		{
			name:        "empty target title returns nil",
			targetID:    "gu-new",
			targetTitle: "",
			wantIDs:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matches := detectOverlappingInFlightWork(tc.targetID, tc.targetTitle, candidates)
			var gotIDs []string
			for _, m := range matches {
				gotIDs = append(gotIDs, m.ID)
			}
			if !reflect.DeepEqual(gotIDs, tc.wantIDs) {
				t.Errorf("detectOverlappingInFlightWork = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

func TestDetectOverlappingInFlightWorkBelowThreshold(t *testing.T) {
	// Shares one scoping word ("scheduler") but otherwise distinct — below the
	// 0.6 threshold, so no false-positive warning.
	candidates := []overlapCandidate{
		{ID: "gu-a", Title: "Tune scheduler capacity caps"},
	}
	matches := detectOverlappingInFlightWork("gu-b", "Profile scheduler dispatch latency", candidates)
	if len(matches) != 0 {
		t.Errorf("expected no matches below threshold, got %v", matches)
	}
}

func TestWarnIfOverlappingWorkInFlightGuards(t *testing.T) {
	// Empty inputs must short-circuit before touching inFlightBeadsForRig.
	orig := inFlightBeadsForRig
	t.Cleanup(func() { inFlightBeadsForRig = orig })
	called := false
	inFlightBeadsForRig = func(_, _ string) []overlapCandidate {
		called = true
		return nil
	}

	warnIfOverlappingWorkInFlight("", "rig", "gu-1", "title")
	warnIfOverlappingWorkInFlight("town", "", "gu-1", "title")
	warnIfOverlappingWorkInFlight("town", "rig", "gu-1", "")
	if called {
		t.Error("inFlightBeadsForRig should not be called when an input is empty")
	}

	// With all inputs present, it consults the (stubbed) in-flight list.
	warnIfOverlappingWorkInFlight("town", "rig", "gu-1", "real title")
	if !called {
		t.Error("inFlightBeadsForRig should be called when all inputs are present")
	}
}
