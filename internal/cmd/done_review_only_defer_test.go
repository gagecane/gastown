package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubBdScriptForReviewOnlyDefer returns a bd stub script. show calls dispatch
// to fixture files in fixtureDir keyed by bead ID; all calls are appended to
// callsLog so tests can assert which subcommands ran.
func stubBdScriptForReviewOnlyDefer(callsLog, fixtureDir string) string {
	return fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
echo "$cmd $*" >> "%s"
case "$cmd" in
  show)
    beadID="$1"
    fixture="%s/$beadID.json"
    if [ -f "$fixture" ]; then
      cat "$fixture"
    else
      echo "[]"
    fi
    ;;
  list)
    echo "[]"
    ;;
  close|update|agent|slot|comments)
    exit 0
    ;;
esac
exit 0
`, callsLog, fixtureDir)
}

// writeBeadFixture writes a JSON array containing a single Issue-shaped object
// to fixtureDir/<id>.json. The bd show stub returns this verbatim.
func writeBeadFixture(t *testing.T, fixtureDir, id, title, status, description string) {
	t.Helper()
	type issue struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	data, err := json.Marshal([]issue{{ID: id, Title: title, Status: status, Description: description}})
	if err != nil {
		t.Fatalf("marshal fixture %s: %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, id+".json"), data, 0644); err != nil {
		t.Fatalf("write fixture %s: %v", id, err)
	}
}

// TestUpdateAgentStateOnDone_ReviewOnlyDeferredClosesNotDefers verifies the
// gu-ybjb fix: when a polecat exits with --status DEFERRED on a hooked bead
// flagged review_only=true (or no_merge=true), updateAgentStateOnDone must
// take the close path rather than the defer-cooldown path.
//
// Pre-fix: the polecat wrapper / formula advised `gt done --status DEFERRED`
// for "report-only tasks" (PRD reviews, plan reviews, code reviews). That
// landed at the defer-cooldown branch, leaving the bead DEFERRED. Convoy
// synthesis depends on legs being terminal, so synthesis blocked until a
// human manually closed the leg.
//
// Post-fix: review_only / no_merge hooked beads are treated like workflow
// step beads on DEFERRED — close, don't defer-cooldown.
func TestUpdateAgentStateOnDone_ReviewOnlyDeferredClosesNotDefers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	cases := []struct {
		name    string
		flagSet string
	}{
		{"review_only_true", "review_only: true"},
		{"no_merge_true", "no_merge: true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()

			if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
				t.Fatalf("mkdir mayor: %v", err)
			}
			beadsDir := filepath.Join(townRoot, ".beads")
			if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
				t.Fatalf("mkdir .beads/locks: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
				t.Fatalf("mkdir gastown: %v", err)
			}
			routes := strings.Join([]string{
				`{"prefix":"gt-","path":"gastown"}`,
				"",
			}, "\n")
			if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
				t.Fatalf("write routes.jsonl: %v", err)
			}

			// Fixture directory for bd show responses.
			fixtureDir := filepath.Join(townRoot, "fixtures")
			if err := os.MkdirAll(fixtureDir, 0755); err != nil {
				t.Fatalf("mkdir fixtures: %v", err)
			}
			// Polecat agent bead (queried by getAgentBeadID via role lookup).
			writeBeadFixture(t, fixtureDir, "gt-gastown-polecat-nitro",
				"Polecat nitro", "open",
				"role_type: polecat\nrig: gastown\nhook_bead: gt-leg-rev\nagent_state: working")
			// Hooked review_only / no_merge bead — the bead under test.
			// Description carries attached_molecule + the flag we're exercising.
			// updateAgentStateOnDone parses this via beads.ParseAttachmentFields.
			writeBeadFixture(t, fixtureDir, "gt-leg-rev",
				"PRD review leg", "in_progress",
				"attached_molecule: gt-wisp-fake\nattached_formula: mol-prd-review\n"+tc.flagSet)
			// Wisp the leg points at via attached_molecule (must exist so the
			// molecule-close branch in updateAgentStateOnDone doesn't error).
			writeBeadFixture(t, fixtureDir, "gt-wisp-fake",
				"Wisp", "open", "")

			binDir := filepath.Join(townRoot, "bin")
			if err := os.MkdirAll(binDir, 0755); err != nil {
				t.Fatalf("mkdir bin: %v", err)
			}
			callsLog := filepath.Join(townRoot, "calls.log")
			script := stubBdScriptForReviewOnlyDefer(callsLog, fixtureDir)
			bdPath := filepath.Join(binDir, "bd")
			if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
				t.Fatalf("write bd stub: %v", err)
			}

			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GT_ROLE", "polecat")
			t.Setenv("GT_RIG", "gastown")
			t.Setenv("GT_POLECAT", "nitro")
			t.Setenv("GT_CREW", "")
			t.Setenv("TMUX_PANE", "")

			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitDeferred, "gt-leg-rev", false)

			data, err := os.ReadFile(callsLog)
			if err != nil {
				t.Fatalf("no calls log: %v", err)
			}
			calls := string(data)

			// Defer-cooldown invocation is `bd update gt-leg-rev --defer=...`.
			// It MUST NOT appear: review_only/no_merge beads should close, not defer.
			if strings.Contains(calls, "update gt-leg-rev --defer=") {
				t.Errorf("review_only/no_merge bead was sent down defer-cooldown path:\n%s", calls)
			}
			// A close call referencing the leg bead must appear.
			closeFound := false
			for _, line := range strings.Split(calls, "\n") {
				if strings.HasPrefix(line, "close ") && strings.Contains(line, "gt-leg-rev") {
					closeFound = true
					break
				}
			}
			if !closeFound {
				t.Errorf("expected close gt-leg-rev for review_only/no_merge bead, got:\n%s", calls)
			}
		})
	}
}

// TestUpdateAgentStateOnDone_NormalDeferredStillDefers verifies the safety net
// is scoped: a non-review_only, non-no_merge, non-workflow-step bead exiting
// DEFERRED still hits the defer-cooldown path. This protects the legitimate
// DEFERRED case (polecat ran out of context, hit unrecoverable error, etc.)
// from accidentally being closed by the gu-ybjb fix.
func TestUpdateAgentStateOnDone_NormalDeferredStillDefers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	fixtureDir := filepath.Join(townRoot, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0755); err != nil {
		t.Fatalf("mkdir fixtures: %v", err)
	}
	writeBeadFixture(t, fixtureDir, "gt-gastown-polecat-nitro",
		"Polecat nitro", "open",
		"role_type: polecat\nrig: gastown\nhook_bead: gt-base-456\nagent_state: working")
	// Normal code-task bead — no review_only, no no_merge, no -wfs- prefix.
	writeBeadFixture(t, fixtureDir, "gt-base-456",
		"Normal task", "in_progress", "")

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	callsLog := filepath.Join(townRoot, "calls.log")
	script := stubBdScriptForReviewOnlyDefer(callsLog, fixtureDir)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nitro")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitDeferred, "gt-base-456", false)

	data, err := os.ReadFile(callsLog)
	if err != nil {
		t.Fatalf("no calls log: %v", err)
	}
	calls := string(data)

	if !strings.Contains(calls, "update gt-base-456 --defer=") {
		t.Errorf("normal DEFERRED bead must hit defer-cooldown path; calls:\n%s", calls)
	}
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "close ") && strings.Contains(line, "gt-base-456") {
			t.Errorf("normal DEFERRED bead must NOT be closed; got close call:\n%s", line)
		}
	}
}
