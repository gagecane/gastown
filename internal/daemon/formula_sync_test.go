package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

// setInstalledHash rewrites .beads/formulas/.installed.json so the recorded
// install hash for one formula matches the given content. This lets a test
// present a runtime copy as "outdated" (current == installed, but embedded
// source has since changed) rather than "operator-modified".
func setInstalledHash(t *testing.T, formulasDir, name string, content []byte) {
	t.Helper()
	path := filepath.Join(formulasDir, ".installed.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .installed.json: %v", err)
	}
	var rec struct {
		Formulas map[string]string `json:"formulas"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse .installed.json: %v", err)
	}
	sum := sha256.Sum256(content)
	rec.Formulas[name] = hex.EncodeToString(sum[:])
	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal .installed.json: %v", err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		t.Fatalf("write .installed.json: %v", err)
	}
}

// newFormulaSyncTestDaemon builds a minimal Daemon wired to townRoot for
// exercising ensureFormulasSynced in isolation (no tmux/Dolt/ctx needed).
func newFormulaSyncTestDaemon(townRoot string) *Daemon {
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}
}

// TestEnsureFormulasSynced_RefreshesOutdatedRuntimeCopy is the gu-ef7e0
// regression test: a formula whose source was fixed (embedded copy differs from
// the deployed runtime copy) must be reconciled into the town runtime registry
// on daemon startup, with no manual `cp SOURCE -> RUNTIME` step. Mirrors the
// real drift: the runtime copy is byte-stale relative to embedded source.
func TestEnsureFormulasSynced_RefreshesOutdatedRuntimeCopy(t *testing.T) {
	townRoot := t.TempDir()

	// Provision the runtime registry from embedded source (install-time state).
	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	formulasDir := filepath.Join(townRoot, ".beads", "formulas")

	// Pick a real formula and capture the embedded (source-of-truth) content.
	const target = "mol-convoy-feed.formula.toml"
	embedded, err := formula.GetEmbeddedFormulaContent(target)
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(%s): %v", target, err)
	}

	runtimePath := filepath.Join(formulasDir, target)

	// Simulate deployment drift: the runtime copy is the pre-fix body and the
	// install record reflects that same pre-fix hash (i.e. it was installed
	// before the source fix landed). Embedded source has since changed, so this
	// presents as "outdated, not operator-modified" — safe to refresh.
	stale := []byte("# stale pre-fix runtime copy\n")
	if err := os.WriteFile(runtimePath, stale, 0644); err != nil {
		t.Fatalf("write stale runtime copy: %v", err)
	}
	setInstalledHash(t, formulasDir, target, stale)

	// Daemon startup self-heal.
	newFormulaSyncTestDaemon(townRoot).ensureFormulasSynced()

	got, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatalf("read synced runtime copy: %v", err)
	}
	if !bytes.Equal(got, embedded) {
		t.Errorf("runtime formula not reconciled to embedded source:\n got: %q\nwant: %q", got, embedded)
	}
}

// TestEnsureFormulasSynced_ReinstallsMissingRuntimeCopy verifies that a runtime
// formula deleted from the registry (e.g. a fresh host that never hand-synced
// it) is reinstalled from embedded source on startup.
func TestEnsureFormulasSynced_ReinstallsMissingRuntimeCopy(t *testing.T) {
	townRoot := t.TempDir()

	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	formulasDir := filepath.Join(townRoot, ".beads", "formulas")
	const target = "mol-convoy-feed.formula.toml"
	runtimePath := filepath.Join(formulasDir, target)

	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime copy: %v", err)
	}

	newFormulaSyncTestDaemon(townRoot).ensureFormulasSynced()

	embedded, err := formula.GetEmbeddedFormulaContent(target)
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(%s): %v", target, err)
	}
	got, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatalf("missing runtime copy was not reinstalled: %v", err)
	}
	if !bytes.Equal(got, embedded) {
		t.Errorf("reinstalled formula does not match embedded source")
	}
}

// TestEnsureFormulasSynced_PreservesOperatorModifiedCopy verifies the self-heal
// never clobbers a deliberate local customization. A runtime formula edited
// after install (and recorded as modified) must survive startup untouched.
func TestEnsureFormulasSynced_PreservesOperatorModifiedCopy(t *testing.T) {
	townRoot := t.TempDir()

	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	formulasDir := filepath.Join(townRoot, ".beads", "formulas")
	const target = "mol-convoy-feed.formula.toml"
	runtimePath := filepath.Join(formulasDir, target)

	// An operator edits the runtime copy. CheckFormulaHealth classifies this as
	// "modified" (current hash != embedded and != installed), so UpdateFormulas
	// must skip it. We assert via the public health report rather than poking at
	// .installed.json internals.
	custom := []byte("# operator local customization — do not clobber\n")
	if err := os.WriteFile(runtimePath, custom, 0644); err != nil {
		t.Fatalf("write operator customization: %v", err)
	}
	report, err := formula.CheckFormulaHealth(townRoot)
	if err != nil {
		t.Fatalf("CheckFormulaHealth: %v", err)
	}
	if report.Modified == 0 {
		t.Fatalf("precondition: expected the edited copy to be classified modified, got report %+v", report)
	}

	newFormulaSyncTestDaemon(townRoot).ensureFormulasSynced()

	got, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatalf("read operator copy: %v", err)
	}
	if !bytes.Equal(got, custom) {
		t.Errorf("operator-modified formula was clobbered by sync:\n got: %q\nwant: %q", got, custom)
	}
}
