// curio-proposer is the Curio Retrospect lane's write-incapable skeleton
// (Curio P2 build 3/6, design gc-tt4p9.6.3).
//
// Retrospect is the OFFLINE, nightly/post-incident counterpart to the live
// Curio Patrol (the curio_dog daemon). Where Patrol watches the world as it
// happens, Retrospect re-reads a FROZEN, closed window of already-emitted
// candidates and (in later builds) hypothesizes root causes with an LLM. This
// build is the skeleton: it wires the read path, the closed-window cursor, and
// the kill switch, and proves three invariants by construction + test. It does
// NOT call an LLM, file beads, or mutate any state.
//
// Three TESTED invariants (see main_test.go):
//
//  1. Write-incapable. This binary's import graph EXCLUDES internal/beads and
//     internal/daemon — the mutation capability is physically absent, not just
//     unused. TestImportGraph_NoWritePath asserts it.
//
//  2. Closed-window cursor. Retrospect only reads candidates strictly OLDER
//     than now minus a safety margin. Live-tailing is forbidden by the
//     closed-window invariant: the cursor is always strictly behind now.
//     TestClosedWindowCursor asserts the margin.
//
//  3. Kill-switch isolation. curio.llm.enabled=false disables THIS lane only;
//     it does not read or touch curio.enabled (the live Patrol's switch).
//     TestKillSwitchIsolation asserts the two switches are independent.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/gastown/internal/curio"
)

// closedWindowMargin is how far strictly behind now the read cursor sits. Any
// candidate written within this trailing margin is considered "in-flight" and
// is NOT read by Retrospect — this is the mechanical enforcement of the
// closed-window invariant (no live-tailing). Sized at one Patrol interval
// (15m, the daemon default) plus headroom so a cycle in progress when
// Retrospect runs is never half-observed.
const closedWindowMargin = 30 * time.Minute

// closedWindowCursor returns the read cutoff: the newest created_at Retrospect
// is allowed to observe. It is now minus closedWindowMargin, so the cursor is
// STRICTLY behind now by at least the margin — the closed-window invariant,
// expressed as a pure function so the test can assert it without a clock or DB.
func closedWindowCursor(now time.Time) time.Time {
	return now.Add(-closedWindowMargin)
}

func main() {
	var (
		townRoot   = flag.String("town-root", "", "Gas Town root directory (default: $GT_TOWN_ROOT, then $GT_TOWN)")
		doltPort   = flag.Int("dolt-port", defaultDoltPort(), "gt Dolt server port")
		dbName     = flag.String("db", "hq", "HQ Dolt database name")
		emitDigest = flag.String("emit-digest", "", "render the deterministic closed-window digest to this path (read-only; no LLM, no filing)")
	)
	flag.Parse()

	root := resolveTownRoot(*townRoot)
	if root == "" {
		fmt.Fprintln(os.Stderr, "curio-proposer: town root not set (pass -town-root or set GT_TOWN_ROOT)")
		os.Exit(2)
	}

	if err := run(root, *doltPort, *dbName, *emitDigest, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "curio-proposer: %v\n", err)
		os.Exit(1)
	}
}

// run is the proposer's body, factored out of main so it takes an explicit
// clock (now) and returns errors instead of exiting — keeping it testable.
//
// Flow: load kill switch -> if LLM lane is off, exit cleanly (no read, no
// touch) -> else open a READ-ONLY candidate view and read the closed window.
// There is NO write path anywhere in this flow: no filing, no LLM, no mutation.
func run(townRoot string, doltPort int, dbName, digestPath string, now time.Time) error {
	cfg, err := loadProposerConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Kill-switch isolation: gate on curio.llm.enabled ONLY. The live Patrol's
	// curio.enabled is deliberately not consulted — disabling the LLM lane must
	// not disable Patrol, and enabling Patrol must not enable this lane. This
	// gate applies to --emit-digest too: a disabled lane emits NOTHING (no file
	// written) and exits 0, matching the design's "llm.enabled=false → exit
	// without emitting" contract.
	if !cfg.llmEnabled() {
		fmt.Println("curio-proposer: curio.llm.enabled=false — Retrospect lane disabled, exiting (live Patrol untouched)")
		return nil
	}

	cutoff := closedWindowCursor(now)

	reader, err := curio.OpenReader("127.0.0.1", doltPort, dbName)
	if err != nil {
		return fmt.Errorf("opening read-only candidate store: %w", err)
	}
	defer func() { _ = reader.Close() }()

	cands, err := reader.ReadCandidatesBefore(cutoff)
	if err != nil {
		return fmt.Errorf("reading closed-window candidates: %w", err)
	}

	// --emit-digest: read outcome history, render the deterministic Markdown+JSON
	// digest, and write it to the path. Still read-only by construction — the
	// Reader exposes no write method and this binary's import graph excludes the
	// mutation packages (TestImportGraph_NoWritePath).
	if digestPath != "" {
		outcomes, err := reader.ReadOutcomeHistory()
		if err != nil {
			return fmt.Errorf("reading outcome history: %w", err)
		}
		// Q5 layer 1 air-gap: drop self-referential candidates BEFORE rendering,
		// so the self-referential data never reaches the digest (and thus never
		// reaches the write-capable agent that consumes it). The predicate is
		// single-sourced with the live suppressed()/isCurioSeries path.
		cands = curio.ExcludeSelfReferential(cands)
		digest := curio.RenderDigest(cutoff, cands, outcomes)
		if err := os.WriteFile(digestPath, []byte(digest), 0o600); err != nil {
			return fmt.Errorf("writing digest to %s: %w", digestPath, err)
		}
		fmt.Printf("curio-proposer: wrote digest (cutoff=%s, %d candidate(s), %d rule(s)) to %s\n",
			cutoff.Format(time.RFC3339), len(cands), len(outcomes), digestPath)
		return nil
	}

	// Skeleton: report what the closed window holds. Hypothesizing (LLM) and
	// filing are later builds (4/6 acceptance, 5/6 shadow, 6/6 filing rights).
	// Nothing here writes — Retrospect is read-only by construction.
	fmt.Printf("curio-proposer: closed-window cursor=%s read %d candidate(s) (read-only; no LLM, no filing)\n",
		cutoff.Format(time.RFC3339), len(cands))
	return nil
}

// resolveTownRoot picks the town root from the flag, then GT_TOWN_ROOT, then
// GT_TOWN. Returns "" when none are set (caller errors out). It does NOT fall
// back to ~/gt: Retrospect runs against an explicit town and must not guess.
func resolveTownRoot(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("GT_TOWN_ROOT"); v != "" {
		return v
	}
	return os.Getenv("GT_TOWN")
}

// defaultDoltPort reads GT_DOLT_PORT, falling back to the gt server default
// (3307) when unset or unparseable — matching the daemon's doltServerPort.
func defaultDoltPort() int {
	if v := os.Getenv("GT_DOLT_PORT"); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil && p > 0 {
			return p
		}
	}
	return 3307
}
