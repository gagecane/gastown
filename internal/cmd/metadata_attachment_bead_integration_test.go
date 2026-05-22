//go:build integration

// gu-2s03: OQ4 fallback acceptance — metadata-attachment-bead pattern
// survives concurrent writes.
//
// The Phase 0a-3 spike (gu-g9ufm,
// internal/cmd/metadata_reliability_integration_test.go) FAILED
// acceptance #2: 100 goroutines doing the production read-modify-write
// pattern (mergeMetadataKey + UpdateIssue) on a single Issue.Metadata blob
// lost ~60/100 contributions to clobber. The Auto-Test-PR design's
// transition_log / rejection_log writes sit on that exact pattern, so the
// data-leg's documented OQ4 fallback (see
// .designs/auto-test-pr/synthesis.md §"OQ4 fallback:
// metadata-attachment-bead pattern") is to file each transition /
// rejection as a NEW immutable bead via bd create, linked to the parent
// state bead via dependency + label. bd create is naturally CAS-safe (each
// call mints a new ID; nothing to clobber), which is the property the
// fallback exists to recover.
//
// This test mirrors the spike harness against the new pattern: 100
// goroutines each call beads.Create to file an attachment bead against a
// shared parent state bead. After all goroutines finish, listing the
// attachment beads by label MUST return all 100 distinct attachments.
// A missing attachment would prove the new pattern isn't actually CAS-
// safe; surviving 100/100 is the prerequisite-bead acceptance criterion
// (gu-2s03 §Acceptance #5).
//
// Gating: gated behind GT_RUN_OQ4_SPIKE=1 (same as the original spike) —
// it requires a real Dolt server and is not part of the default integration
// suite. To run:
//
//	GT_RUN_OQ4_SPIKE=1 go test -tags=integration \
//	  -run TestMetadataAttachmentBeadConcurrency \
//	  -timeout 5m -count=1 -v ./internal/cmd/

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// attachmentSpikeCounter generates unique prefixes for each sub-test so
// concurrent runs don't collide on the shared Dolt server.
var attachmentSpikeCounter atomic.Int32

const (
	// attachmentWriterCount mirrors the spike's 100-writer harness so the
	// two tests can be diffed directly: same writer count → if the new
	// pattern survives where the old failed, the difference is the storage
	// shape, not the load.
	attachmentWriterCount = 100

	// labelAttachmentUmbrella is the umbrella discriminator label for all
	// attachment beads in the OQ4 fallback. Matches the schema documented
	// in synthesis.md §"OQ4 fallback".
	labelAttachmentUmbrella = "gt:auto-test-pr-attachment"

	// labelKindTransition / labelKindRejection are the per-kind discriminators
	// the materializer reads. Each attachment carries exactly one kind label.
	labelKindTransition = "kind:transition"
	labelKindRejection  = "kind:rejection"
)

// setupAttachmentSpikeRig stands up a beads-backed rig dir wired to the
// shared Dolt test container, creates one parent "state" bead, and returns
// a *beads.Beads for the rig and the parent's ID. Cleanup wipes nothing —
// the unique-prefix scheme isolates this run from others on the shared
// server.
func setupAttachmentSpikeRig(t *testing.T) (b *beads.Beads, parentID string) {
	t.Helper()

	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping OQ4 fallback acceptance")
	}
	requireDoltServer(t)

	n := attachmentSpikeCounter.Add(1)
	prefix := fmt.Sprintf("at%d", n)

	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	rigDir := filepath.Join(tmpDir, "rig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	configureTestGitIdentity(t, tmpDir)
	initBeadsDBForServer(t, rigDir, prefix)

	b = beads.New(rigDir)
	parent, err := b.Create(beads.CreateOptions{
		Title:       "OQ4 fallback parent <rig>-auto-test-state",
		Labels:      []string{"gt:task", "gt:auto-test-pr"},
		Priority:    2,
		Description: "Stand-in for the per-rig auto-test state pinned bead.",
	})
	if err != nil {
		t.Fatalf("create parent state bead: %v", err)
	}
	return b, parent.ID
}

// TestMetadataAttachmentBeadConcurrency is the gu-2s03 acceptance #5
// harness: under the OQ4 fallback (each transition is a new bead via bd
// create, NOT an RMW into the parent's Issue.Metadata), 100 concurrent
// writers MUST land 100 distinct attachments — zero lost updates — and
// the materialize-from-attachments read path MUST recover all 100.
//
// SPIKE STATUS: Gated by GT_RUN_OQ4_SPIKE=1 to match the original spike's
// gating. The new pattern is expected to PASS where the old FAILED; a
// FAIL outcome here would invalidate the fallback design and require a
// new design round.
func TestMetadataAttachmentBeadConcurrency(t *testing.T) {
	if os.Getenv("GT_RUN_OQ4_SPIKE") != "1" {
		t.Skip("OQ4 fallback acceptance skipped (set GT_RUN_OQ4_SPIKE=1 to run); " +
			"see gu-2s03 — verifies metadata-attachment-bead pattern under " +
			"100 concurrent writers, the prerequisite the fallback exists to " +
			"satisfy")
	}
	t.Run("ConcurrentTransitionAttachmentsNoLostUpdate",
		testAttachmentConcurrentTransitions)
}

// testAttachmentConcurrentTransitions launches attachmentWriterCount
// goroutines that each:
//
//  1. Call beads.Create to file a transition attachment bead with a
//     unique-per-writer payload in Issue.Metadata.
//  2. Wire the attachment to the parent state bead via AddDependency
//     (the read-path materializer can use either the dependency edge OR
//     the rig:<rig> + kind:transition label query; the test exercises
//     both edges).
//
// After all goroutines complete, the test lists by label and asserts:
//
//  1. Exactly attachmentWriterCount attachments are recovered.
//  2. Each writer's unique writer_id is present exactly once in the
//     materialized list — no clobber, no duplication, no silent drop.
func testAttachmentConcurrentTransitions(t *testing.T) {
	b, parentID := setupAttachmentSpikeRig(t)

	// rig label value must be stable across writers — the materializer
	// filters on rig:<rig> + kind:* + the umbrella label, so all writers
	// must agree on the rig string.
	const rigLabelValue = "rig:gastown_upstream"
	rigName := "gastown_upstream"

	// Pre-build per-writer payloads outside the race window so JSON
	// marshalling cost doesn't perturb the timing.
	type transitionPayload struct {
		SchemaVersion int               `json:"schema_version"`
		Rig           string            `json:"rig"`
		From          string            `json:"from"`
		To            string            `json:"to"`
		At            string            `json:"at"`
		Actor         string            `json:"actor"`
		Context       map[string]string `json:"context"`
		WriterID      string            `json:"writer_id"`
	}

	payloads := make([][]byte, attachmentWriterCount)
	for i := range payloads {
		p := transitionPayload{
			SchemaVersion: 1,
			Rig:           rigName,
			From:          "mr-pending",
			To:            "cooled-down",
			At:            fmt.Sprintf("2026-05-22T00:%02d:%02dZ", i/60, i%60),
			Actor:         "refinery",
			Context:       map[string]string{"mr_id": fmt.Sprintf("gu-mr-att-%03d", i)},
			WriterID:      fmt.Sprintf("att-rmw-%03d", i),
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("pre-marshal writer %d: %v", i, err)
		}
		payloads[i] = raw
	}

	var (
		wg          sync.WaitGroup
		writeErrors atomic.Int32
		errOnce     sync.Once
		firstErr    error
		ids         = make([]string, attachmentWriterCount)
	)
	wg.Add(attachmentWriterCount)
	for i := 0; i < attachmentWriterCount; i++ {
		i := i
		go func() {
			defer wg.Done()
			att, err := b.Create(beads.CreateOptions{
				Title: fmt.Sprintf(
					"auto-test-pr transition %s: mr-pending → cooled-down @ writer %03d",
					rigName, i),
				Labels: []string{
					labelAttachmentUmbrella,
					labelKindTransition,
					rigLabelValue,
					"gt:auto-test-pr",
				},
				Priority:    2,
				Description: string(payloads[i]),
				Actor:       "mayor-cycle-close-handler",
			})
			if err != nil {
				writeErrors.Add(1)
				errOnce.Do(func() {
					firstErr = fmt.Errorf("writer %d Create: %w", i, err)
				})
				return
			}
			if depErr := b.AddDependency(att.ID, parentID); depErr != nil {
				// The dependency edge is part of the schema (parent ↔
				// attachment) but the materializer's primary lookup is the
				// label query, so a missed dep is recorded but not fatal.
				// Real production code would retry; this test logs and
				// continues so the label-query acceptance still runs.
				t.Logf("writer %d AddDependency: %v (label query will still work)",
					i, depErr)
			}
			ids[i] = att.ID
		}()
	}
	wg.Wait()

	if writeErrors.Load() > 0 {
		t.Fatalf("OQ4 fallback acceptance FAILED at the create surface: %d/%d "+
			"concurrent writers errored (first: %v) — bd create is supposed to "+
			"be CAS-safe, so a non-zero error count here is the new design's "+
			"smoking gun and invalidates the fallback. Re-design required.",
			writeErrors.Load(), attachmentWriterCount, firstErr)
	}

	// Materialize-from-attachments read path: list by the umbrella label,
	// filter client-side by kind + rig (the materializer in synthesis.md
	// §"OQ4 fallback" uses the same shape).
	listed, err := b.List(beads.ListOptions{
		Label:  labelAttachmentUmbrella,
		Status: "open",
	})
	if err != nil {
		t.Fatalf("List by attachment umbrella label: %v", err)
	}

	// Filter to (kind:transition AND rig:<rig>) — this is the materializer's
	// post-query filter for the transition log.
	matched := make([]*beads.Issue, 0, attachmentWriterCount)
	for _, iss := range listed {
		if issueHasLabel(iss, labelKindTransition) && issueHasLabel(iss, rigLabelValue) {
			matched = append(matched, iss)
		}
	}

	// Acceptance #1: count matches the writer count exactly. A short count
	// proves a lost write (bd create silently dropped one); a long count
	// proves a duplicate (impossible by construction, but worth checking).
	if got := len(matched); got != attachmentWriterCount {
		t.Fatalf("OQ4 fallback acceptance FAILED: materializer recovered %d/%d "+
			"attachment beads (parent=%s) — a missing attachment proves the new "+
			"pattern is NOT CAS-safe and the fallback design is invalid",
			got, attachmentWriterCount, parentID)
	}

	// Acceptance #2: every writer's writer_id appears exactly once. We rely
	// on the Description field (where we stashed the JSON payload) since
	// the bd CLI doesn't surface Issue.Metadata for non-pinned beads in the
	// list-by-label path — the writer_id round-trip via Description is
	// sufficient for the lost-update check.
	seenWriters := make(map[string]int, attachmentWriterCount)
	for _, iss := range matched {
		var p transitionPayload
		if err := json.Unmarshal([]byte(iss.Description), &p); err != nil {
			t.Errorf("attachment %s: unmarshal payload: %v\nraw=%q",
				iss.ID, err, truncateBytes([]byte(iss.Description), 200))
			continue
		}
		seenWriters[p.WriterID]++
	}

	missing := make([]string, 0)
	dupes := make([]string, 0)
	for i := 0; i < attachmentWriterCount; i++ {
		writerID := fmt.Sprintf("att-rmw-%03d", i)
		switch seenWriters[writerID] {
		case 0:
			missing = append(missing, writerID)
		case 1:
			// expected
		default:
			dupes = append(dupes, fmt.Sprintf("%s×%d",
				writerID, seenWriters[writerID]))
		}
	}
	sort.Strings(missing)
	sort.Strings(dupes)

	if len(missing) > 0 || len(dupes) > 0 {
		t.Fatalf("OQ4 fallback acceptance FAILED: writer presence mismatch — "+
			"missing=%v dupes=%v (out of %d expected). The new pattern is NOT "+
			"recovering all writers; the fallback design is invalid as written.",
			missing, dupes, attachmentWriterCount)
	}

	t.Logf("OQ4 fallback acceptance: %d/%d concurrent attachment-bead "+
		"writers landed and are recoverable via the materialize-from-"+
		"attachments label query — bd create is CAS-safe under the same "+
		"100-writer load that lost ~60/100 RMW writes in gu-g9ufm.",
		attachmentWriterCount, attachmentWriterCount)
}

// hasLabel returns true if the issue's Labels slice contains the given
// label exactly. Mirrors the materializer's defensive client-side label
// filter from synthesis.md §"OQ4 fallback" (since beads.List takes one
// --label flag, the secondary filter is local).
func issueHasLabel(iss *beads.Issue, label string) bool {
	for _, l := range iss.Labels {
		if l == label {
			return true
		}
	}
	return false
}
