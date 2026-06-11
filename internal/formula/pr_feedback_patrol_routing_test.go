package formula

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Phase 2-21 (gu-iluw): Integration tests for feedback-patrol label-keyed
// dispatch routing — regression protection for R9.
//
// The label-keyed dispatch decision in mol-pr-feedback-patrol is not a Go
// function; it ships as the bash body of the `dispatch-work` step (see
// gu-vvl4y / gu-4pyq). Asserting only that the step's prose *contains*
// keywords (TestPRFeedbackPatrolDispatchStep*) cannot catch a logic
// regression — e.g. a flipped condition that routes labeled MRs to the
// generic handler or unlabeled MRs to the revise pipeline.
//
// These tests close that gap by executing the real shipped bash. They
// extract the fenced bash blocks straight out of the dispatch-work step,
// substitute the formula vars exactly as the runtime would, point PATH at
// stub `gt`/`bd`/`gh` binaries that record their invocations, and assert
// which dispatch path actually fired for a fixture MR finding:
//
//   - Labeled MR (gt:auto-test-pr) + feature flag on  -> revise pipeline
//   - Unlabeled MR                                     -> generic dispatch
//   - Labeled MR + feature flag off                    -> generic dispatch
//
// The harness only adapts the step's hard-coded /tmp findings path to a
// per-test temp dir; the routing logic itself runs verbatim as shipped.

// extractBashBlocks returns the contents of every ```bash fenced code block
// in markdown, in document order.
func extractBashBlocks(markdown string) []string {
	var blocks []string
	lines := strings.Split(markdown, "\n")
	var cur []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```bash" {
				inBlock = true
				cur = nil
			}
			continue
		}
		if trimmed == "```" {
			inBlock = false
			blocks = append(blocks, strings.Join(cur, "\n"))
			continue
		}
		cur = append(cur, line)
	}
	return blocks
}

// writeRoutingStub writes an executable shell stub to dir/name.
func writeRoutingStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// renderDispatchScript pulls the dispatch-work step's bash out of the real
// formula, substitutes the formula vars, and rewrites the hard-coded
// findings path to point at the test's findings file.
func renderDispatchScript(t *testing.T, findingsPath string) string {
	t.Helper()
	f, err := ParseFile("formulas/mol-pr-feedback-patrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	step := f.GetStep("dispatch-work")
	if step == nil {
		t.Fatal("dispatch-work step not found")
	}

	blocks := extractBashBlocks(step.Description)
	if len(blocks) < 3 {
		t.Fatalf("expected >=3 bash blocks in dispatch-work, got %d", len(blocks))
	}
	script := strings.Join(blocks, "\n")

	// Substitute the formula vars exactly as the runtime would.
	repl := strings.NewReplacer(
		"{{rig}}", "testrig",
		"{{repo}}", "acme/app",
		"{{patrol_label}}", "pr-feedback-patrol",
		"{{max_open_beads}}", "20",
		"/tmp/patrol-findings.txt", findingsPath,
	)
	script = repl.Replace(script)

	// Guard: any unresolved {{var}} would mean the formula grew a new var
	// this harness doesn't substitute — fail loudly rather than run a
	// script with literal braces in it.
	if leftover := regexp.MustCompile(`\{\{[a-zA-Z_][a-zA-Z0-9_]*\}\}`).FindAllString(script, -1); len(leftover) > 0 {
		t.Fatalf("unsubstituted template vars in dispatch-work bash: %v", leftover)
	}
	return script
}

func TestPRFeedbackPatrolDispatchRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dispatch-work step is POSIX bash; not portable to Windows")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	// Stub bodies. They append every invocation to $GT_TEST_LOG (a file, so
	// it never pollutes command-substitution stdout) and emit only the
	// stdout the dispatch logic captures.
	const gtStub = `#!/usr/bin/env bash
echo "gt $*" >> "$GT_TEST_LOG"
case "$1" in
  rig) echo "$GT_STUB_FLAG" ;;  # gt rig settings get <rig> <key>
esac
exit 0
`
	const bdStub = `#!/usr/bin/env bash
echo "bd $*" >> "$GT_TEST_LOG"
case "$1" in
  list)
    # Only the MR-bead lookup carries both the gt:auto-test-pr label and
    # --format id; everything else (safety valve, dedup guard) returns
    # nothing so the counts come back 0.
    if [[ "$*" == *"gt:auto-test-pr"* && "$*" == *"format"* ]]; then
      [ -n "$BD_STUB_MR_BEAD" ] && echo "$BD_STUB_MR_BEAD"
    fi
    ;;
  create) echo "gu-created1" ;;
esac
exit 0
`
	const ghStub = `#!/usr/bin/env bash
echo "gh $*" >> "$GT_TEST_LOG"
echo "$GH_STUB_LABELS"
exit 0
`

	cases := []struct {
		name       string
		labels     string // GH_STUB_LABELS — labels on the fixture PR
		flag       string // GT_STUB_FLAG — rig feature flag value
		mrBead     string // BD_STUB_MR_BEAD — MR bead the lookup returns
		wantRevise bool   // expect route to mol-auto-test-pr-pipeline mode=revise
	}{
		{
			name:       "labeled MR with flag on routes to revise pipeline",
			labels:     "gt:auto-test-pr,needs-tests",
			flag:       "true",
			mrBead:     "gu-mrbead1",
			wantRevise: true,
		},
		{
			name:       "unlabeled MR routes to generic dispatch",
			labels:     "needs-tests,bug",
			flag:       "true",
			mrBead:     "",
			wantRevise: false,
		},
		{
			name:       "labeled MR with flag off routes to generic dispatch",
			labels:     "gt:auto-test-pr",
			flag:       "false",
			mrBead:     "gu-mrbead1",
			wantRevise: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			binDir := filepath.Join(tmp, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatalf("mkdir bin: %v", err)
			}
			writeRoutingStub(t, binDir, "gt", gtStub)
			writeRoutingStub(t, binDir, "bd", bdStub)
			writeRoutingStub(t, binDir, "gh", ghStub)

			// One review-feedback finding for PR #42, in the exact
			// colon-delimited shape the patrol's earlier steps emit.
			findings := filepath.Join(tmp, "patrol-findings.txt")
			line := "review-feedback:42:https://github.com/acme/app/pull/42:Add tests for foo\n"
			if err := os.WriteFile(findings, []byte(line), 0o644); err != nil {
				t.Fatalf("write findings: %v", err)
			}

			logPath := filepath.Join(tmp, "calls.log")
			script := renderDispatchScript(t, findings)

			cmd := exec.Command(bashPath, "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GT_TEST_LOG="+logPath,
				"GT_STUB_FLAG="+tc.flag,
				"GH_STUB_LABELS="+tc.labels,
				"BD_STUB_MR_BEAD="+tc.mrBead,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("dispatch script failed: %v\noutput:\n%s", err, out)
			}

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read call log: %v", err)
			}
			calls := string(logBytes)

			reviseInvoked := strings.Contains(calls, "gt formula run mol-auto-test-pr-pipeline") &&
				strings.Contains(calls, "mode=revise")
			genericInvoked := strings.Contains(calls, "bd create") &&
				strings.Contains(calls, "gt sling testrig")

			if tc.wantRevise {
				if !reviseInvoked {
					t.Errorf("expected revise-pipeline dispatch, not found.\ncalls:\n%s", calls)
				}
				if !strings.Contains(calls, "mr_bead="+tc.mrBead) {
					t.Errorf("revise dispatch missing mr_bead=%s.\ncalls:\n%s", tc.mrBead, calls)
				}
				if genericInvoked {
					t.Errorf("labeled MR should NOT hit generic dispatch (bd create / gt sling).\ncalls:\n%s", calls)
				}
			} else {
				if !genericInvoked {
					t.Errorf("expected generic dispatch (bd create + gt sling), not found.\ncalls:\n%s", calls)
				}
				if reviseInvoked {
					t.Errorf("unlabeled/flag-off MR should NOT route to revise pipeline.\ncalls:\n%s", calls)
				}
			}
		})
	}
}
