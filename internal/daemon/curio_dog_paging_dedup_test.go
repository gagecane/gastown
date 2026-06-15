package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/curio"
)

// TestPageOverseer_PassesDedupSignatureArgs is the L2 half of the end-to-end
// judgment-lane re-fire suppression proof (gc-e2uvyr.4). The other halves are:
//
//   - L1 (engine): a latched judgment breaker re-emits ActionJudgmentBump with a
//     STABLE DedupKey every cycle — proven in
//     internal/curio/paging_test.go (TestJudgmentLane_LatchesClosedAndBumps,
//     and the multi-cycle TestJudgmentLane_StableDedupKeyAcrossManyBumps added
//     by this task).
//   - L3 (escalate): `gt escalate --dedup --signature=X` bumps occurrence_count
//     on the open bead instead of creating a new one — proven against real
//     bd/Dolt in internal/cmd/curio_judgment_suppression_integration_test.go.
//
// This test pins the SEAM between L1 and L3: the daemon's real pageOverseer must
// translate a PageAction into a `gt escalate` invocation that actually carries
// the dedup flags. Without --dedup AND --signature=<stable key>, L3's
// suppression can never engage and every cycle would mint a fresh Overseer page
// — the exact failure the NO-GO note feared. The existing daemon paging tests
// all STUB pageOverseer, so this argv contract was previously unverified.
//
// We don't run a real `gt` here (that's L3's integration test); we intercept the
// argv by pointing d.gtPath at a recording shim and asserting the flags.
func TestPageOverseer_PassesDedupSignatureArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("argv-recording shim uses a POSIX shell script")
	}

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")

	// A shim that records every argument it was invoked with, one per line, then
	// exits 0. It stands in for the real `gt` binary so we can assert exactly
	// which flags pageOverseer constructs.
	shim := filepath.Join(dir, "gt")
	script := "#!/bin/sh\n" +
		": > \"" + argsFile + "\"\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + argsFile + "\"; done\n" +
		"exit 0\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil { //nolint:gosec // test shim must be executable
		t.Fatalf("write shim: %v", err)
	}

	d := &Daemon{
		logger: log.New(io.Discard, "", 0),
		gtPath: shim,
	}

	const key = "curio:judgment:abc123stablekey"
	d.pageOverseer(PageAction{
		Kind:     curio.ActionJudgmentBump,
		Lane:     curio.LaneJudgment,
		Severity: "high",
		DedupKey: key,
		Summary:  "judgment lane still firing: 9 occurrence(s) across 2 cluster(s)",
	})

	raw, err := os.ReadFile(argsFile) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("pageOverseer did not invoke gtPath (no args file): %v", err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	// First token must be the escalate subcommand.
	if len(args) == 0 || args[0] != "escalate" {
		t.Fatalf("expected first arg 'escalate', got %v", args)
	}

	// --dedup must be present: without it, runEscalate never enters the
	// signature-dedup branch and every cycle creates a fresh bead.
	if !hasArg(args, "--dedup") {
		t.Errorf("missing --dedup flag; re-fires would NOT dedup. args=%v", args)
	}

	// --signature must carry the engine's stable DedupKey verbatim. The bump
	// path in runEscalate matches on this exact string, so any rewriting here
	// would split one incident into N beads.
	if got := flagValue(args, "--signature"); got != key {
		t.Errorf("--signature = %q, want stable DedupKey %q (args=%v)", got, key, args)
	}

	// --fingerprint is also wired to the same key as a second-line dedup anchor;
	// assert it too so a future refactor can't silently drop the coupling.
	if got := flagValue(args, "--fingerprint"); got != key {
		t.Errorf("--fingerprint = %q, want %q (args=%v)", got, key, args)
	}

	// Severity must propagate from the action (graded high/critical upstream).
	if got := flagValue(args, "--severity"); got != "high" {
		t.Errorf("--severity = %q, want %q", got, "high")
	}
}

// TestPageOverseer_StableArgsAcrossManyBumps confirms the argv contract holds
// across a sustained re-fire: the engine, once latched, hands pageOverseer the
// SAME DedupKey every cycle, so every `gt escalate` call carries an identical
// --signature. This is what lets L3 collapse N cycles into one bead + N-1
// occurrence bumps. A per-cycle-varying signature here would defeat dedup even
// though each individual call looks correct.
func TestPageOverseer_StableArgsAcrossManyBumps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("argv-recording shim uses a POSIX shell script")
	}

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	shim := filepath.Join(dir, "gt")
	// Append-mode shim: record the --signature value of each invocation so we
	// can assert all cycles agree.
	script := "#!/bin/sh\n" +
		"sig=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --signature) sig=\"$2\"; shift 2;;\n" +
		"    --signature=*) sig=\"${1#--signature=}\"; shift;;\n" +
		"    *) shift;;\n" +
		"  esac\n" +
		"done\n" +
		"printf '%s\\n' \"$sig\" >> \"" + argsFile + "\"\n" +
		"exit 0\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil { //nolint:gosec // test shim
		t.Fatalf("write shim: %v", err)
	}

	d := &Daemon{logger: log.New(io.Discard, "", 0), gtPath: shim}

	// Drive the REAL engine through a trip + several bumps, feeding each emitted
	// action through the REAL pageOverseer. This exercises L1→L2 jointly.
	e := curio.NewPagingEngine()
	now := time.Now()
	cands := []curio.Candidate{
		newJudgmentCand("cluster-a"),
		newJudgmentCand("cluster-b"),
		newJudgmentCand("cluster-c"),
	}
	const cycles = 5
	emitted := 0
	for i := 0; i < cycles; i++ {
		ts := now.Add(time.Duration(i) * time.Minute)
		for _, a := range e.Decide(cands, ts) {
			if a.Lane != curio.LaneJudgment {
				continue
			}
			emitted++
			d.pageOverseer(a)
		}
	}

	if emitted < 2 {
		t.Fatalf("expected a trip + >=1 bump across %d cycles, got %d judgment pages", cycles, emitted)
	}

	raw, err := os.ReadFile(argsFile) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("no signatures recorded: %v", err)
	}
	sigs := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(sigs) != emitted {
		t.Fatalf("recorded %d signatures but emitted %d pages", len(sigs), emitted)
	}
	first := sigs[0]
	if first == "" {
		t.Fatalf("first escalate call carried an empty --signature: %v", sigs)
	}
	for i, s := range sigs {
		if s != first {
			t.Errorf("signature drifted at cycle %d: %q != %q — re-fires would split into multiple beads", i, s, first)
		}
	}
}

// hasArg reports whether args contains the exact flag token.
func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// flagValue returns the value for a "--flag value" pair (the form pageOverseer
// uses) or a "--flag=value" pair, or "" if absent.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

// newJudgmentCand builds a judgment-lane candidate (no verifier) with a distinct
// state key. Mirrors the curio package's test helper; redefined here because it
// is unexported there.
func newJudgmentCand(stateHash string) curio.Candidate {
	return curio.Candidate{
		RuleID:      "bead_merged_not_landed",
		Fingerprint: stateHash,
		StateHash:   stateHash,
		Summary:     "judgment finding " + stateHash,
	}
}
