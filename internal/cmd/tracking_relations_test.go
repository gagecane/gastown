package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrackingDependsOnID_CrossRigWrapsExternal(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"ag-\",\"path\":\"agentcompany/.beads\"}\n"), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	got := trackingDependsOnID(townRoot, "ag-95s.1")
	want := "external:ag:ag-95s.1"
	if got != want {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, want)
	}
}

func TestTrackingDependsOnID_HQStaysLocal(t *testing.T) {
	townRoot := t.TempDir()
	got := trackingDependsOnID(townRoot, "hq-cv-test")
	if got != "hq-cv-test" {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, "hq-cv-test")
	}
}

// TestNormalizeTownRoot_AcceptsBothForms regression-tests gu-7xqy. Some callers
// (formula.go, sling_convoy.go) pass <townRoot>/.beads while others pass the
// town root itself. The helper must accept both, otherwise route lookup ends up
// at <townRoot>/.beads/.beads which doesn't exist and silently fails — causing
// cross-rig convoy leg tracking to report "issue not found".
func TestNormalizeTownRoot_AcceptsBothForms(t *testing.T) {
	townRoot := t.TempDir()

	t.Run("plain town root passthrough", func(t *testing.T) {
		got := normalizeTownRoot(townRoot)
		if got != townRoot {
			t.Fatalf("normalizeTownRoot(%q) = %q, want %q", townRoot, got, townRoot)
		}
	})

	t.Run("townRoot with .beads suffix is stripped", func(t *testing.T) {
		got := normalizeTownRoot(filepath.Join(townRoot, ".beads"))
		if got != townRoot {
			t.Fatalf("normalizeTownRoot(%q/.beads) = %q, want %q",
				townRoot, got, townRoot)
		}
	})
}

// TestAddTrackingRelation_AcceptsBeadsDirAsTownRoot regression-tests gu-7xqy by
// simulating a formula-style caller that passes a .beads directory in place of
// the town root. The resulting cross-rig issue ID must still be wrapped as
// "external:<prefix>:<id>" — proving the helper is no longer fooled by the
// trailing .beads segment when looking up routes.
func TestAddTrackingRelation_AcceptsBeadsDirAsTownRoot(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"cacr-","path":"casc_crud/mayor/rig"}`+"\n"),
		0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Calling trackingDependsOnID with the .beads-suffixed townRoot must still
	// resolve the cross-rig prefix correctly. Before the fix this returned the
	// bare bead ID (because route lookup ran against townRoot/.beads/.beads),
	// which made store.AddDependency look for the leg in HQ and fail with
	// "issue not found".
	got := trackingDependsOnID(normalizeTownRoot(beadsDir), "cacr-leg-chqp2")
	want := "external:cacr:cacr-leg-chqp2"
	if got != want {
		t.Fatalf("trackingDependsOnID(beadsDir, leg) = %q, want %q", got, want)
	}
}
