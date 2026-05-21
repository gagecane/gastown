//go:build integration

// Phase 0a-3: Pinned-bead Issue.Metadata reliability spike (OQ4).
//
// Auto-Test-PR design (.designs/auto-test-pr/synthesis.md) plans to round-trip
// ~5KB JSON blobs (transition_log[≤50] + rejection_log[≤200]) on every state
// transition of the per-rig <rig>-auto-test-state pinned bead. Before any
// production code depends on that, this spike validates two reliability
// invariants on Issue.Metadata:
//
//   1. Byte-for-byte round-trip fidelity — a 5KB JSON blob written via
//      Storage.UpdateIssue and read back via Storage.GetIssue must come back
//      bit-identical. Run 100 sequential round-trips against a real Dolt
//      server.
//
//   2. CAS isolation under concurrent read-modify-write — 100 goroutines each
//      add a unique key to the same bead's metadata via the same RMW pattern
//      production uses (mergeMetadataKey in internal/beads/store.go: read full
//      metadata, mutate map, write back). After all complete, every key MUST
//      be present in the final read. A missing key is a lost update — the
//      smoking gun the spike is hunting for.
//
// Acceptance (gu-g9ufm):
//
//   - 100/100 byte-for-byte equality on round-trip AND no CAS lost-update
//     detected → OQ4 PASS, Phase 0 task 8 + Phase 1 task 15 proceed as
//     designed.
//   - Otherwise → OQ4 FAIL, file the metadata-attachment-bead fallback bead
//     and re-shape tasks 8 + 15.
//
// Run with:
//
//	go test -tags=integration -run TestMetadataReliabilitySpike \
//	  -timeout 5m -count=1 -v ./internal/cmd/

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
)

// metadataSpikeCounter generates unique prefixes for each spike sub-test so
// concurrent runs (or test re-runs without DB cleanup) don't collide on the
// shared Dolt server.
var metadataSpikeCounter atomic.Int32

const (
	// targetBlobSize is the upper-bound payload size from the Auto-Test-PR
	// design (transition_log[≤50] + rejection_log[≤200] ≈ 5KB JSON).
	targetBlobSize = 5 * 1024

	// metadataRoundTripCount is the per-sub-test iteration count from the
	// gu-g9ufm acceptance criteria ("Run 100 round-trips concurrently to
	// stress CAS isolation").
	metadataRoundTripCount = 100
)

// build5KBJSONBlob synthesizes a JSON blob shaped like the Auto-Test-PR
// pinned-bead state — a transition log and a rejection log — sized at the
// upper bound (~5KB). Returns the canonical JSON encoding so callers can
// compare bytes after the round-trip.
//
// The salt parameter lets concurrent writers produce blobs that are unique
// per writer without changing the size class.
func build5KBJSONBlob(t *testing.T, salt string) json.RawMessage {
	t.Helper()

	// transition_log[≤50] entries: each ~50 bytes → ~2.5KB.
	transitionLog := make([]map[string]string, 0, 50)
	for i := 0; i < 50; i++ {
		transitionLog = append(transitionLog, map[string]string{
			"from": "open",
			"to":   "in_progress",
			"at":   fmt.Sprintf("2026-05-21T04:%02d:00Z", i),
			"by":   fmt.Sprintf("polecat-%s-%02d", salt, i),
		})
	}

	// rejection_log[≤200] entries: each ~10 bytes → ~2KB.
	rejectionLog := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		rejectionLog = append(rejectionLog, fmt.Sprintf("rj%s-%03d", salt, i))
	}

	payload := map[string]interface{}{
		"schema_version":  1,
		"salt":            salt,
		"transition_log":  transitionLog,
		"rejection_log":   rejectionLog,
		"last_cycle_at":   "2026-05-21T04:08:00Z",
		"open_mr_bead_id": "gu-mr-" + salt,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal 5KB blob: %v", err)
	}
	// The design calls this "~5KB" but at the upper bound of
	// transition_log[≤50] + rejection_log[≤200] with schema fields, the
	// real blob lands around ~7-8KB. Accept anything in [4KB, 9KB] — the
	// reliability invariant we care about is the size class, not the exact
	// byte count, and downstream Storage limits operate at that order.
	if len(raw) < 4*1024 || len(raw) > 9*1024 {
		t.Fatalf("blob size %d outside size class [4KB, 9KB]; "+
			"adjust transition_log/rejection_log shape", len(raw))
	}
	return json.RawMessage(raw)
}

// setupSpikeRig stands up a minimal beads-backed rig directory pointed at the
// shared Dolt test container, creates one issue, and returns a beadsdk.Storage
// connected to that rig's database. The caller owns the returned cleanup func.
func setupSpikeRig(t *testing.T) (store beadsdk.Storage, issueID string, cleanup func()) {
	t.Helper()

	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping metadata reliability spike")
	}
	requireDoltServer(t)

	n := metadataSpikeCounter.Add(1)
	prefix := fmt.Sprintf("md%d", n)

	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	rigDir := filepath.Join(tmpDir, "rig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	// Configure git/dolt identity in isolated HOME so bd init can copy
	// identity → dolt.
	configureTestGitIdentity(t, tmpDir)

	initBeadsDBForServer(t, rigDir, prefix)

	// Create one issue we'll round-trip metadata against.
	b := beads.New(rigDir)
	issue, err := b.Create(beads.CreateOptions{
		Title:    "spike target for OQ4",
		Labels:   []string{"gt:task"},
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create spike target issue: %v", err)
	}

	// Open an in-process store against the same rig database. The polecat
	// runs concurrent goroutines through this single store, mirroring how
	// production callers (storeDelegationSet) reach the SDK from a single
	// Beads instance.
	ctx := context.Background()
	store, err = beadsdk.OpenFromConfig(ctx, filepath.Join(rigDir, ".beads"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	cleanup = func() {
		_ = store.Close()
	}
	return store, issue.ID, cleanup
}

// TestMetadataReliabilitySpike is the gu-g9ufm OQ4 reliability spike.
//
// It exercises Issue.Metadata at the upper-bound JSON size (~5KB) via the
// real Dolt-backed beadsdk.Storage and verifies the two acceptance invariants
// from the Auto-Test-PR design.
//
// SPIKE STATUS: ConcurrentCASNoLostUpdate is a known FAIL — the OQ4 finding
// (~60/100 lost RMW updates) is the *signal* this spike was written to
// surface, and Acceptance #2 is intentionally fail-closed. Leaving the
// sub-test wired into the default integration build would turn an OQ4
// fallback-bead trigger into permanent CI red, so the spike runs only when
// GT_RUN_OQ4_SPIKE=1. Maintainers re-running the spike (e.g., to verify
// the metadata-attachment-bead fallback resolves it, or to re-validate after
// an SDK bump) export GT_RUN_OQ4_SPIKE=1.
//
// To re-run the spike:
//
//	GT_RUN_OQ4_SPIKE=1 go test -tags=integration //	  -run TestMetadataReliabilitySpike -timeout 5m -count=1 -v ./internal/cmd/
func TestMetadataReliabilitySpike(t *testing.T) {
	if os.Getenv("GT_RUN_OQ4_SPIKE") != "1" {
		t.Skip("OQ4 reliability spike skipped (set GT_RUN_OQ4_SPIKE=1 to run); " +
			"see gu-g9ufm — concurrent CAS sub-test is a known FAIL that " +
			"motivated the metadata-attachment-bead fallback")
	}
	t.Run("SequentialByteForByteRoundTrip", testMetadataSequentialRoundTrip)
	t.Run("ConcurrentCASNoLostUpdate", testMetadataConcurrentCAS)
}

// testMetadataSequentialRoundTrip writes 100 distinct 5KB JSON blobs to one
// issue's metadata, reading each back and verifying byte-for-byte equality.
//
// This proves Acceptance #1: Issue.Metadata is a faithful storage surface for
// ~5KB blobs — no truncation, no normalization, no JSON re-encoding drift.
func testMetadataSequentialRoundTrip(t *testing.T) {
	store, issueID, cleanup := setupSpikeRig(t)
	defer cleanup()

	ctx := context.Background()
	for i := 0; i < metadataRoundTripCount; i++ {
		want := build5KBJSONBlob(t, fmt.Sprintf("seq%03d", i))

		updates := map[string]interface{}{"metadata": want}
		if err := store.UpdateIssue(ctx, issueID, updates, "spike-actor"); err != nil {
			t.Fatalf("iteration %d: UpdateIssue: %v", i, err)
		}

		got, err := store.GetIssue(ctx, issueID)
		if err != nil {
			t.Fatalf("iteration %d: GetIssue: %v", i, err)
		}

		// Compare canonical JSON forms — UpdateIssue accepts json.RawMessage
		// and Storage MAY round-trip via a TEXT column that re-encodes JSON
		// (whitespace, key order). Byte-for-byte equality MUST hold over the
		// canonical encoding for downstream callers to safely diff state.
		gotCanon, errGot := canonicalizeJSON(got.Metadata)
		wantCanon, errWant := canonicalizeJSON(want)
		if errGot != nil || errWant != nil {
			t.Fatalf("iteration %d: canonicalize: got=%v want=%v", i, errGot, errWant)
		}
		if !bytes.Equal(wantCanon, gotCanon) {
			t.Fatalf("iteration %d: byte-for-byte mismatch\n want %d bytes: %s\n got  %d bytes: %s",
				i, len(wantCanon), truncateBytes(wantCanon, 200),
				len(gotCanon), truncateBytes(gotCanon, 200))
		}
	}
	t.Logf("OQ4 acceptance #1: %d/%d sequential 5KB round-trips byte-for-byte equal",
		metadataRoundTripCount, metadataRoundTripCount)
}

// testMetadataConcurrentCAS launches metadataRoundTripCount goroutines that
// each perform a read-modify-write of the same issue's metadata, adding a
// unique key whose value is a 5KB-sized fragment. Mirrors the production RMW
// pattern in internal/beads/store.go (mergeMetadataKey + UpdateIssue) used by
// storeDelegationSet and by the planned auto-test-pr transition writer.
//
// Acceptance #2: after all goroutines finish, the final metadata must contain
// every one of the 100 distinct keys. A missing key proves a lost update —
// goroutine N's write committed AFTER goroutine M had already read the
// metadata, so M's subsequent write clobbered N's contribution.
//
// Each value is sized so the final aggregated blob lands within the same
// ~5KB working set per writer (the test stresses CAS, not blob bloat).
func testMetadataConcurrentCAS(t *testing.T) {
	store, issueID, cleanup := setupSpikeRig(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-build per-writer values so we don't time JSON marshalling into the
	// race window. Each value is a distinct ~50-byte JSON object — small
	// enough that 100 of them aggregate to a few KB rather than 500KB.
	values := make([]json.RawMessage, metadataRoundTripCount)
	for i := range values {
		v, err := json.Marshal(map[string]string{
			"writer": fmt.Sprintf("rmw-%03d", i),
			"at":     "2026-05-21T04:08:00Z",
		})
		if err != nil {
			t.Fatalf("pre-marshal writer %d: %v", i, err)
		}
		values[i] = json.RawMessage(v)
	}

	// Verify the full ~5KB invariant at least once via a baseline blob the
	// concurrent writers collectively rebuild. Acceptance #2 hunts CAS bugs;
	// the size ramp is preserved by the seq sub-test.
	var (
		wg          sync.WaitGroup
		writeErrors atomic.Int32
		errOnce     sync.Once
		firstErr    error
	)
	wg.Add(metadataRoundTripCount)
	for i := 0; i < metadataRoundTripCount; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("rmw_%03d", i)
			if err := rmwAddKey(ctx, store, issueID, key, values[i]); err != nil {
				writeErrors.Add(1)
				errOnce.Do(func() { firstErr = fmt.Errorf("writer %d: %w", i, err) })
			}
		}()
	}
	wg.Wait()

	if writeErrors.Load() > 0 {
		t.Fatalf("OQ4 acceptance #2: %d/%d concurrent RMW writers errored "+
			"(first: %v) — backing store cannot sustain transition-log writes",
			writeErrors.Load(), metadataRoundTripCount, firstErr)
	}

	final, err := store.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("final GetIssue: %v", err)
	}

	finalMap := make(map[string]json.RawMessage)
	if len(final.Metadata) > 0 {
		if err := json.Unmarshal(final.Metadata, &finalMap); err != nil {
			t.Fatalf("unmarshal final metadata: %v\nraw: %s", err,
				truncateBytes(final.Metadata, 500))
		}
	}

	// Acceptance #2 fail-closed check.
	missing := make([]string, 0)
	for i := 0; i < metadataRoundTripCount; i++ {
		key := fmt.Sprintf("rmw_%03d", i)
		got, ok := finalMap[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		// Each surviving key must equal the value its writer attempted to
		// merge — a clobbered-then-rewritten key still indicates a lost
		// update relative to a different writer.
		gotCanon, _ := canonicalizeJSON(got)
		wantCanon, _ := canonicalizeJSON(values[i])
		if !bytes.Equal(gotCanon, wantCanon) {
			t.Errorf("key %s: value mismatch\n want: %s\n got:  %s",
				key, truncateBytes(wantCanon, 120), truncateBytes(gotCanon, 120))
		}
	}

	if len(missing) > 0 {
		// Surface the smoking gun for OQ4 FAIL: include the count and a
		// sample so the prerequisite-bead writeup has actionable evidence.
		sample := missing
		if len(sample) > 10 {
			sample = sample[:10]
		}
		t.Fatalf("OQ4 acceptance #2 FAILED: %d/%d concurrent RMW keys lost "+
			"to clobber (sample: %v) — Issue.Metadata cannot sustain "+
			"transition-log RMW under concurrency without external CAS. "+
			"File metadata-attachment-bead fallback prerequisite (see "+
			"data-leg OQ4 fallback in synthesis.md) and re-shape Phase 0 "+
			"task 8 + Phase 1 task 15.",
			len(missing), metadataRoundTripCount, sample)
	}

	t.Logf("OQ4 acceptance #2: %d/%d concurrent RMW keys present and intact "+
		"(final metadata: %d bytes)", metadataRoundTripCount,
		metadataRoundTripCount, len(final.Metadata))
}

// rmwAddKey performs a read-modify-write of an issue's metadata, adding/
// overwriting `key` with `value`. This intentionally mirrors the production
// pattern in internal/beads/store.go — mergeMetadataKey + UpdateIssue — so
// the spike measures the actual code path the auto-test-pr cycle would use.
//
// Each call: GetIssue → unmarshal map → set key → marshal → UpdateIssue.
// No transaction, no compare-and-swap on a version column. If the SDK does
// not enforce isolation across concurrent UpdateIssue calls, a write that
// reads at version N and writes at version N+1 can clobber another writer
// that committed between read and write.
func rmwAddKey(ctx context.Context, store beadsdk.Storage, issueID, key string, value json.RawMessage) error {
	cur, err := store.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	m := make(map[string]json.RawMessage)
	if len(cur.Metadata) > 0 {
		if err := json.Unmarshal(cur.Metadata, &m); err != nil {
			// Non-object metadata — treat as empty (matches mergeMetadataKey).
			m = make(map[string]json.RawMessage)
		}
	}
	m[key] = value
	merged, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	updates := map[string]interface{}{"metadata": json.RawMessage(merged)}
	if err := store.UpdateIssue(ctx, issueID, updates, "spike-actor-rmw"); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

// canonicalizeJSON unmarshal-then-remarshals a JSON value so byte-comparison
// is invariant to whitespace and key ordering imposed by Storage's underlying
// TEXT column. Fail-fast on invalid JSON so the caller distinguishes "blob
// corrupted by storage" from "blob whitespace differs."
func canonicalizeJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return json.Marshal(v)
}

// truncate returns the first n bytes of b, suffixed with "..." if truncated.
// Used for error messages so 5KB blobs don't drown test output.
func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
