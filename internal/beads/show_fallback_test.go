package beads

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// installMockBDShowPerBeadsDir installs a mock `bd` that returns show output
// keyed on the BEADS_DIR env var. This lets tests exercise the routed-vs-local
// Show paths with different responses for each DB.
//
// outputs maps absolute BEADS_DIR paths to stdout payloads. If a BEADS_DIR is
// absent from the map the mock exits with code 1 and writes "not found" to
// stderr, which wrapError translates to ErrNotFound.
func installMockBDShowPerBeadsDir(t *testing.T, outputs map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for bd")
	}

	binDir := t.TempDir()

	// Build a case statement dispatching on BEADS_DIR.
	script := "#!/bin/sh\n" +
		"cmd=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		"    --*) ;;\n" +
		"    *) cmd=\"$arg\"; break ;;\n" +
		"  esac\n" +
		"done\n" +
		"\n" +
		"case \"$cmd\" in\n" +
		"  version) exit 0 ;;\n" +
		"  show)\n" +
		"    case \"$BEADS_DIR\" in\n"
	for beadsDir, out := range outputs {
		script += "      \"" + beadsDir + "\")\n"
		script += "        cat <<'__MOCK_BD_EOF__'\n"
		script += out + "\n"
		script += "__MOCK_BD_EOF__\n"
		script += "        exit 0\n"
		script += "        ;;\n"
	}
	script += "      *)\n"
	script += "        echo \"Issue not found\" >&2\n"
	script += "        exit 1\n"
	script += "        ;;\n"
	script += "    esac\n"
	script += "    ;;\n"
	script += "  *) exit 0 ;;\n"
	script += "esac\n"

	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeTown creates a minimal town structure with routes.jsonl under tmpDir
// and returns the town beads dir and the rig beads dir.
func writeTown(t *testing.T, tmpDir, prefix, rigRelPath string) (townBeadsDir, rigBeadsDir string) {
	t.Helper()

	// mayor/town.json makes FindTownRoot happy.
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	townBeadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}

	// Route the prefix to the town root ("."), mirroring how gt- is routed in
	// real towns. The bead it names lives outside this route.
	routes := `{"prefix":"` + prefix + `","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	rigBeadsDir = filepath.Join(tmpDir, rigRelPath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}

	return townBeadsDir, rigBeadsDir
}

// TestShow_FallsBackToLocalDirWhenRoutedShowMisses covers gu-vkg3: when a
// pinned bead's prefix routes via routes.jsonl to a database that does not
// actually contain the bead, Show must fall back to the Beads wrapper's
// local resolved beads dir rather than returning ErrNotFound.
//
// Repro matches the real bug: a "gt-" prefixed pinned bead was created by
// mayor inside a rig DB even though routes.jsonl says "gt-" lives at the
// town root. Without the fallback, `gt mol detach <id>` and `gt doctor --fix`
// both error with "issue not found" and the stale attachment cannot be
// cleaned up.
func TestShow_FallsBackToLocalDirWhenRoutedShowMisses(t *testing.T) {
	tmpDir := t.TempDir()
	townBeadsDir, rigBeadsDir := writeTown(t, tmpDir, "gt-", "casc_constructs/mayor/rig")

	// bd show for the bead succeeds only from the rig DB — the town DB
	// (the routed target) doesn't have it.
	rigShowOutput := `[{"id":"gt-d7df6bb3","title":"Witness Patrol","status":"pinned","description":"attached_molecule: mol-witness-patrol\nattached_formula: mol-witness-patrol\nattached_at: 2026-05-01T00:00:00Z\n"}]`
	installMockBDShowPerBeadsDir(t, map[string]string{
		rigBeadsDir: rigShowOutput,
		// townBeadsDir intentionally omitted so the mock exits 1 → ErrNotFound.
	})

	// Build a Beads wrapper rooted at the rig. This mirrors how doctor's
	// Fix path constructs its wrapper (beads.New(filepath.Dir(rig .beads))).
	// We pass the explicit beadsDir to short-circuit any walk-up confusion
	// from the temp dir.
	rigWorkDir := filepath.Dir(rigBeadsDir)
	b := NewWithBeadsDir(rigWorkDir, rigBeadsDir)

	// Sanity: routing resolves to town, confirming the fallback path is what's
	// under test.
	if got := ResolveRoutingTarget(tmpDir, "gt-d7df6bb3", rigBeadsDir); got != townBeadsDir {
		t.Fatalf("precondition: routed target = %q, want %q (town)", got, townBeadsDir)
	}

	issue, err := b.Show("gt-d7df6bb3")
	if err != nil {
		t.Fatalf("Show(gt-d7df6bb3) = %v, want success via local fallback", err)
	}
	if issue == nil || issue.ID != "gt-d7df6bb3" {
		t.Fatalf("Show returned unexpected issue: %+v", issue)
	}
	if issue.Title != "Witness Patrol" {
		t.Fatalf("Show returned wrong bead title %q", issue.Title)
	}
}

// TestShow_ReturnsNotFoundWhenNeitherDirHasBead ensures the fallback is
// genuinely defensive and does not mask a truly missing bead: both the
// routed target AND the local fallback miss, Show must return ErrNotFound.
func TestShow_ReturnsNotFoundWhenNeitherDirHasBead(t *testing.T) {
	tmpDir := t.TempDir()
	_, rigBeadsDir := writeTown(t, tmpDir, "gt-", "casc_constructs/mayor/rig")

	installMockBDShowPerBeadsDir(t, map[string]string{
		// No entries — every show returns not-found.
	})

	rigWorkDir := filepath.Dir(rigBeadsDir)
	b := NewWithBeadsDir(rigWorkDir, rigBeadsDir)

	_, err := b.Show("gt-does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Show(missing) = %v, want ErrNotFound", err)
	}
}

// TestShow_PrefersRoutedResultOverLocal ensures that when the routed target
// contains the bead, Show uses it and does not consult the local dir. This
// protects against cases where a prefix legitimately routes elsewhere and
// the local dir happens to contain an unrelated bead with a colliding ID.
func TestShow_PrefersRoutedResultOverLocal(t *testing.T) {
	tmpDir := t.TempDir()
	townBeadsDir, rigBeadsDir := writeTown(t, tmpDir, "gt-", "casc_constructs/mayor/rig")

	// Different titles in each DB so we can tell which one answered.
	routedOutput := `[{"id":"gt-d7df6bb3","title":"From Town","status":"pinned","description":""}]`
	localOutput := `[{"id":"gt-d7df6bb3","title":"From Rig","status":"pinned","description":""}]`
	installMockBDShowPerBeadsDir(t, map[string]string{
		townBeadsDir: routedOutput,
		rigBeadsDir:  localOutput,
	})

	rigWorkDir := filepath.Dir(rigBeadsDir)
	b := NewWithBeadsDir(rigWorkDir, rigBeadsDir)

	issue, err := b.Show("gt-d7df6bb3")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if issue.Title != "From Town" {
		t.Fatalf("Show returned %q, want 'From Town' (routed target must win)", issue.Title)
	}
}
