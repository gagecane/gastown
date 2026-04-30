package daemon

// Unit tests for internal/daemon/compactor_dog.go.
//
// Scope: pure helpers and config parsing only. The SQL-driven step state
// machine (compactDatabase, surgicalRebase, compactorFetchAndVerify,
// compactorForcePush, compactorRunGC, etc.) requires a live Dolt server and
// is intentionally exercised by higher-level integration tests — see the
// ZFC Exemption note on runCompactorDog in compactor_dog.go for rationale.
//
// These tests cover:
//   - shortHash: display-hash truncation
//   - compactorDogInterval / Threshold / Mode / KeepRecent: config helpers,
//     including nil-safety, default fallback, and override behavior.
//   - compactorDatabases: three-tier fallback (compactor → wisp_reaper →
//     reaper.DefaultDatabases), driven by a Daemon literal.
//   - isConcurrentWriteError: classification of Dolt rebase graph-change
//     errors (the surgical-rebase retry predicate).
//   - parseRebaseOrder2: DECIMAL-string to int conversion for dolt_rebase
//     rebase_order bounds.
//   - IsPatrolEnabled("compactor_dog"): exemption / opt-in semantics at the
//     daemon patrol registry level.

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/reaper"
)

// --- shortHash --------------------------------------------------------------

func TestShortHash(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short", "abc", "abc"},
		{"exactly_eight", "abcdefgh", "abcdefgh"},
		{"long_truncated", "abcdefghijk", "abcdefgh"},
		{"dolt_hash_truncated",
			"h2b3m4n5p6q7r8s9t0u1v2w3x4y5z6a7",
			"h2b3m4n5",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shortHash(tc.in)
			if got != tc.want {
				t.Errorf("shortHash(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- compactorDogInterval ---------------------------------------------------

func TestCompactorDogInterval_Default(t *testing.T) {
	if got := compactorDogInterval(nil); got != defaultCompactorDogInterval {
		t.Errorf("nil config: want %v, got %v", defaultCompactorDogInterval, got)
	}
}

func TestCompactorDogInterval_NilPatrols(t *testing.T) {
	cfg := &DaemonPatrolConfig{}
	if got := compactorDogInterval(cfg); got != defaultCompactorDogInterval {
		t.Errorf("nil patrols: want %v, got %v", defaultCompactorDogInterval, got)
	}
}

func TestCompactorDogInterval_NilCompactorDog(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if got := compactorDogInterval(cfg); got != defaultCompactorDogInterval {
		t.Errorf("nil compactor_dog: want %v, got %v", defaultCompactorDogInterval, got)
	}
}

func TestCompactorDogInterval_Configured(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, IntervalStr: "6h"},
		},
	}
	if got := compactorDogInterval(cfg); got != 6*time.Hour {
		t.Errorf("configured 6h: got %v", got)
	}
}

func TestCompactorDogInterval_InvalidFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, IntervalStr: "not-a-duration"},
		},
	}
	if got := compactorDogInterval(cfg); got != defaultCompactorDogInterval {
		t.Errorf("invalid string: want default %v, got %v", defaultCompactorDogInterval, got)
	}
}

func TestCompactorDogInterval_ZeroFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, IntervalStr: "0s"},
		},
	}
	if got := compactorDogInterval(cfg); got != defaultCompactorDogInterval {
		t.Errorf("zero string: want default %v, got %v", defaultCompactorDogInterval, got)
	}
}

func TestCompactorDogInterval_EmptyStringFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, IntervalStr: ""},
		},
	}
	if got := compactorDogInterval(cfg); got != defaultCompactorDogInterval {
		t.Errorf("empty string: want default %v, got %v", defaultCompactorDogInterval, got)
	}
}

// --- compactorDogThreshold --------------------------------------------------

func TestCompactorDogThreshold_Default(t *testing.T) {
	if got := compactorDogThreshold(nil); got != defaultCompactorCommitThreshold {
		t.Errorf("nil config: want %d, got %d", defaultCompactorCommitThreshold, got)
	}
}

func TestCompactorDogThreshold_NilPatrols(t *testing.T) {
	cfg := &DaemonPatrolConfig{}
	if got := compactorDogThreshold(cfg); got != defaultCompactorCommitThreshold {
		t.Errorf("nil patrols: want %d, got %d", defaultCompactorCommitThreshold, got)
	}
}

func TestCompactorDogThreshold_Configured(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, Threshold: 4096},
		},
	}
	if got := compactorDogThreshold(cfg); got != 4096 {
		t.Errorf("threshold 4096: got %d", got)
	}
}

func TestCompactorDogThreshold_ZeroFallsBack(t *testing.T) {
	// A threshold of 0 is treated as "unset" (see implementation: >0 check).
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, Threshold: 0},
		},
	}
	if got := compactorDogThreshold(cfg); got != defaultCompactorCommitThreshold {
		t.Errorf("zero threshold: want default %d, got %d", defaultCompactorCommitThreshold, got)
	}
}

func TestCompactorDogThreshold_NegativeFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, Threshold: -1},
		},
	}
	if got := compactorDogThreshold(cfg); got != defaultCompactorCommitThreshold {
		t.Errorf("negative threshold: want default %d, got %d", defaultCompactorCommitThreshold, got)
	}
}

// --- compactorDogMode -------------------------------------------------------

func TestCompactorDogMode_Default(t *testing.T) {
	// Nil config / nil patrols / unset mode all default to flatten.
	if got := compactorDogMode(nil); got != "flatten" {
		t.Errorf("nil config: want flatten, got %q", got)
	}
	if got := compactorDogMode(&DaemonPatrolConfig{}); got != "flatten" {
		t.Errorf("nil patrols: want flatten, got %q", got)
	}
	if got := compactorDogMode(&DaemonPatrolConfig{Patrols: &PatrolsConfig{}}); got != "flatten" {
		t.Errorf("nil compactor_dog: want flatten, got %q", got)
	}
}

func TestCompactorDogMode_Surgical(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, Mode: "surgical"},
		},
	}
	if got := compactorDogMode(cfg); got != "surgical" {
		t.Errorf("mode surgical: got %q", got)
	}
}

func TestCompactorDogMode_UnknownFallsBackToFlatten(t *testing.T) {
	// Only "surgical" is a recognized override; anything else → flatten.
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, Mode: "bogus"},
		},
	}
	if got := compactorDogMode(cfg); got != "flatten" {
		t.Errorf("unknown mode: want flatten, got %q", got)
	}
}

func TestCompactorDogMode_EmptyFallsBackToFlatten(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, Mode: ""},
		},
	}
	if got := compactorDogMode(cfg); got != "flatten" {
		t.Errorf("empty mode: want flatten, got %q", got)
	}
}

// --- compactorDogKeepRecent -------------------------------------------------

func TestCompactorDogKeepRecent_Default(t *testing.T) {
	if got := compactorDogKeepRecent(nil); got != 50 {
		t.Errorf("nil config: want 50, got %d", got)
	}
	if got := compactorDogKeepRecent(&DaemonPatrolConfig{}); got != 50 {
		t.Errorf("nil patrols: want 50, got %d", got)
	}
	if got := compactorDogKeepRecent(&DaemonPatrolConfig{Patrols: &PatrolsConfig{}}); got != 50 {
		t.Errorf("nil compactor_dog: want 50, got %d", got)
	}
}

func TestCompactorDogKeepRecent_Configured(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, KeepRecent: 25},
		},
	}
	if got := compactorDogKeepRecent(cfg); got != 25 {
		t.Errorf("keep_recent 25: got %d", got)
	}
}

func TestCompactorDogKeepRecent_ZeroFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, KeepRecent: 0},
		},
	}
	if got := compactorDogKeepRecent(cfg); got != 50 {
		t.Errorf("zero keep_recent: want default 50, got %d", got)
	}
}

func TestCompactorDogKeepRecent_NegativeFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true, KeepRecent: -10},
		},
	}
	if got := compactorDogKeepRecent(cfg); got != 50 {
		t.Errorf("negative keep_recent: want default 50, got %d", got)
	}
}

// --- compactorDatabases -----------------------------------------------------
//
// compactorDatabases walks a three-tier fallback:
//   1. patrols.compactor_dog.databases
//   2. patrols.wisp_reaper.databases
//   3. reaper.DefaultDatabases
//
// Only d.patrolConfig is read, so a zero-value Daemon literal is sufficient.

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestCompactorDatabases_NilConfig(t *testing.T) {
	d := &Daemon{}
	got := d.compactorDatabases()
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(reaper.DefaultDatabases)) {
		t.Errorf("nil config: want %v, got %v", reaper.DefaultDatabases, got)
	}
}

func TestCompactorDatabases_NilPatrols(t *testing.T) {
	d := &Daemon{patrolConfig: &DaemonPatrolConfig{}}
	got := d.compactorDatabases()
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(reaper.DefaultDatabases)) {
		t.Errorf("nil patrols: want %v, got %v", reaper.DefaultDatabases, got)
	}
}

func TestCompactorDatabases_EmptyCompactorDogFallsThrough(t *testing.T) {
	// compactor_dog config present but Databases empty → fall to wisp_reaper,
	// which is nil here, so fall all the way to reaper.DefaultDatabases.
	d := &Daemon{
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				CompactorDog: &CompactorDogConfig{Enabled: true},
			},
		},
	}
	got := d.compactorDatabases()
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(reaper.DefaultDatabases)) {
		t.Errorf("empty compactor_dog databases: want %v, got %v", reaper.DefaultDatabases, got)
	}
}

func TestCompactorDatabases_CompactorDogOverride(t *testing.T) {
	want := []string{"alpha", "beta"}
	d := &Daemon{
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				CompactorDog: &CompactorDogConfig{Enabled: true, Databases: want},
				WispReaper:   &WispReaperConfig{Databases: []string{"should-not-win"}},
			},
		},
	}
	got := d.compactorDatabases()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("compactor_dog override: want %v, got %v", want, got)
	}
}

func TestCompactorDatabases_WispReaperFallback(t *testing.T) {
	want := []string{"wr-one", "wr-two", "wr-three"}
	d := &Daemon{
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				// Compactor_dog configured but with no Databases → falls through.
				CompactorDog: &CompactorDogConfig{Enabled: true},
				WispReaper:   &WispReaperConfig{Databases: want},
			},
		},
	}
	got := d.compactorDatabases()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wisp_reaper fallback: want %v, got %v", want, got)
	}
}

func TestCompactorDatabases_WispReaperOnly(t *testing.T) {
	// No compactor_dog config at all; wisp_reaper provides the list.
	want := []string{"only-wisp"}
	d := &Daemon{
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				WispReaper: &WispReaperConfig{Databases: want},
			},
		},
	}
	got := d.compactorDatabases()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wisp_reaper only: want %v, got %v", want, got)
	}
}

// --- isConcurrentWriteError -------------------------------------------------
//
// The surgical rebase retry loop depends on correctly classifying
// concurrent-write errors. False negatives cause user-visible failures
// when a retry would have succeeded; false positives cause infinite
// retry loops on unrelated errors.

func TestIsConcurrentWriteError_Nil(t *testing.T) {
	if isConcurrentWriteError(nil) {
		t.Error("nil error should not be classified as concurrent write")
	}
}

func TestIsConcurrentWriteError_KnownPatterns(t *testing.T) {
	// Patterns documented in surgicalRebase / surgicalRebaseOnce / the
	// package-level doc comment on isConcurrentWriteError.
	cases := []string{
		"rebase execution failed: some detail",
		"wrapped: rebase execution failed",
		"concurrency abort: main HEAD moved",
		"the commit graph changed underneath",
		"commit graph was rewritten",
		"cannot rebase: working tree dirty",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			if !isConcurrentWriteError(errors.New(msg)) {
				t.Errorf("expected concurrent-write classification for %q", msg)
			}
		})
	}
}

func TestIsConcurrentWriteError_WrappedError(t *testing.T) {
	// fmt.Errorf-wrapped errors must still match — we rely on the
	// formatted message containing the needle, which %w preserves.
	inner := errors.New("rebase execution failed")
	wrapped := fmt.Errorf("surgical step 5: %w", inner)
	if !isConcurrentWriteError(wrapped) {
		t.Errorf("wrapped error %q should match", wrapped.Error())
	}
}

func TestIsConcurrentWriteError_UnrelatedErrors(t *testing.T) {
	// These must NOT be classified as concurrent-write errors — otherwise
	// surgicalRebase would retry forever on unrelated failures.
	cases := []string{
		"connection refused",
		"table not found: foo",
		"permission denied",
		"EOF",
		"context deadline exceeded",
		"unknown database: missing",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			if isConcurrentWriteError(errors.New(msg)) {
				t.Errorf("unexpectedly classified %q as concurrent write", msg)
			}
		})
	}
}

// --- parseRebaseOrder2 ------------------------------------------------------
//
// Dolt returns dolt_rebase.rebase_order as DECIMAL. The MySQL driver yields
// strings like "1.00". parseRebaseOrder2 normalizes them to int via
// float rounding so the squash-threshold arithmetic works.

func TestParseRebaseOrder2_IntegerStrings(t *testing.T) {
	min, max, err := parseRebaseOrder2("1", "10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if min != 1 || max != 10 {
		t.Errorf("want (1,10), got (%d,%d)", min, max)
	}
}

func TestParseRebaseOrder2_DecimalStrings(t *testing.T) {
	// Canonical Dolt output: "1.00" / "100.00".
	min, max, err := parseRebaseOrder2("1.00", "100.00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if min != 1 || max != 100 {
		t.Errorf("want (1,100), got (%d,%d)", min, max)
	}
}

func TestParseRebaseOrder2_RoundingBehavior(t *testing.T) {
	// math.Round is banker-agnostic half-away-from-zero.
	min, max, err := parseRebaseOrder2("1.4", "9.6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if min != 1 {
		t.Errorf("want min=1 (round(1.4)), got %d", min)
	}
	if max != 10 {
		t.Errorf("want max=10 (round(9.6)), got %d", max)
	}
}

func TestParseRebaseOrder2_InvalidMin(t *testing.T) {
	_, _, err := parseRebaseOrder2("not-a-number", "10")
	if err == nil {
		t.Fatal("expected error for invalid min")
	}
}

func TestParseRebaseOrder2_InvalidMax(t *testing.T) {
	_, _, err := parseRebaseOrder2("1", "also-bad")
	if err == nil {
		t.Fatal("expected error for invalid max")
	}
}

func TestParseRebaseOrder2_EmptyStrings(t *testing.T) {
	// strconv.ParseFloat rejects empty strings — both args produce errors.
	if _, _, err := parseRebaseOrder2("", "10"); err == nil {
		t.Error("expected error for empty min")
	}
	if _, _, err := parseRebaseOrder2("1", ""); err == nil {
		t.Error("expected error for empty max")
	}
}

func TestParseRebaseOrder2_MinEqualsMax(t *testing.T) {
	// A single-commit rebase plan produces min == max. Not an error per se;
	// the caller (surgicalRebaseOnce) checks squashThreshold <= minOrder
	// and aborts the rebase rather than rejecting the parse.
	min, max, err := parseRebaseOrder2("5.00", "5.00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if min != max || min != 5 {
		t.Errorf("want (5,5), got (%d,%d)", min, max)
	}
}

// --- IsPatrolEnabled: compactor_dog ----------------------------------------
//
// compactor_dog is opt-in. We verify each nil-guard rung of the check.

func TestIsPatrolEnabled_CompactorDog_NilConfig(t *testing.T) {
	if IsPatrolEnabled(nil, "compactor_dog") {
		t.Error("nil config: expected compactor_dog disabled")
	}
}

func TestIsPatrolEnabled_CompactorDog_NilPatrols(t *testing.T) {
	if IsPatrolEnabled(&DaemonPatrolConfig{}, "compactor_dog") {
		t.Error("nil patrols: expected compactor_dog disabled")
	}
}

func TestIsPatrolEnabled_CompactorDog_NilCompactorDog(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if IsPatrolEnabled(cfg, "compactor_dog") {
		t.Error("nil compactor_dog: expected disabled")
	}
}

func TestIsPatrolEnabled_CompactorDog_Explicit(t *testing.T) {
	enabled := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(enabled, "compactor_dog") {
		t.Error("explicit enabled: expected true")
	}

	disabled := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(disabled, "compactor_dog") {
		t.Error("explicit disabled: expected false")
	}
}

// --- Constants sanity check -------------------------------------------------
//
// The daemon relies on these constants having sensible values. A refactor
// that accidentally zeroes one of them would silently break compaction.

func TestCompactorConstants(t *testing.T) {
	if defaultCompactorDogInterval <= 0 {
		t.Error("defaultCompactorDogInterval must be positive")
	}
	if defaultCompactorCommitThreshold <= 0 {
		t.Error("defaultCompactorCommitThreshold must be positive")
	}
	if compactorQueryTimeout <= 0 {
		t.Error("compactorQueryTimeout must be positive")
	}
	if compactorGCTimeout <= 0 {
		t.Error("compactorGCTimeout must be positive")
	}
	if compactorPushTimeout <= 0 {
		t.Error("compactorPushTimeout must be positive")
	}
	if compactorBranchName == "" {
		t.Error("compactorBranchName must be non-empty")
	}
	if surgicalMaxRetries < 0 {
		t.Error("surgicalMaxRetries must be non-negative")
	}
	// GC needs more headroom than per-query; push needs more than per-query.
	if compactorGCTimeout <= compactorQueryTimeout {
		t.Errorf("compactorGCTimeout (%v) must exceed compactorQueryTimeout (%v)",
			compactorGCTimeout, compactorQueryTimeout)
	}
	if compactorPushTimeout <= compactorQueryTimeout {
		t.Errorf("compactorPushTimeout (%v) must exceed compactorQueryTimeout (%v)",
			compactorPushTimeout, compactorQueryTimeout)
	}
}
