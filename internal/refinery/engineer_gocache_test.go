package refinery

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

// gocache scoping isolates concurrent rig gates so one rig's build can't
// evict another's cache entries mid-link (gu-sav6u).

func TestRigGoCache_ScopedToRigName(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GT_GATE_GOCACHE_BASE", base)

	alpha := &Engineer{rig: &rig.Rig{Name: "alpha"}}
	beta := &Engineer{rig: &rig.Rig{Name: "beta"}}

	gotAlpha := alpha.rigGoCache()
	gotBeta := beta.rigGoCache()

	if want := filepath.Join(base, "alpha"); gotAlpha != want {
		t.Errorf("alpha rigGoCache = %q, want %q", gotAlpha, want)
	}
	if want := filepath.Join(base, "beta"); gotBeta != want {
		t.Errorf("beta rigGoCache = %q, want %q", gotBeta, want)
	}
	if gotAlpha == gotBeta {
		t.Errorf("expected distinct per-rig caches, both = %q", gotAlpha)
	}
}

func TestRigGoCache_EmptyWhenNoRigName(t *testing.T) {
	t.Setenv("GT_GATE_GOCACHE_BASE", t.TempDir())

	cases := map[string]*Engineer{
		"nil rig":    {},
		"empty name": {rig: &rig.Rig{Name: ""}},
	}
	for name, e := range cases {
		if got := e.rigGoCache(); got != "" {
			t.Errorf("%s: rigGoCache = %q, want empty", name, got)
		}
	}
}

func TestRigGoCache_OptOut(t *testing.T) {
	t.Setenv("GT_GATE_GOCACHE_BASE", t.TempDir())
	t.Setenv("GT_GATE_GOCACHE", "off")

	e := &Engineer{rig: &rig.Rig{Name: "alpha"}}
	if got := e.rigGoCache(); got != "" {
		t.Errorf("rigGoCache with opt-out = %q, want empty", got)
	}
}

func TestGateBuildEnv_OverridesGocache(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GT_GATE_GOCACHE_BASE", base)
	// A stale inherited GOCACHE must be replaced, not duplicated.
	t.Setenv("GOCACHE", "/some/shared/go-build")

	e := &Engineer{rig: &rig.Rig{Name: "alpha"}}
	env := e.gateBuildEnv()
	if env == nil {
		t.Fatal("gateBuildEnv returned nil, want scoped env")
	}

	want := "GOCACHE=" + filepath.Join(base, "alpha")
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOCACHE=") {
			count++
			if kv != want {
				t.Errorf("GOCACHE entry = %q, want %q", kv, want)
			}
		}
	}
	if count != 1 {
		t.Errorf("found %d GOCACHE entries, want exactly 1", count)
	}
}

// gs-bc08: even an unscoped engineer (no rig → no GOCACHE scoping, TMPDIR
// override disabled) must still scrub the refinery's production Dolt-routing
// vars, so a gate `go test ./...` can't inherit GT_DOLT_PORT/BEADS_DOLT_PORT and
// leak orphan databases onto the shared production Dolt server. gateBuildEnv
// therefore never returns nil — it always returns a scrubbed env.
func TestGateBuildEnv_StripsDoltEnvWhenUnscoped(t *testing.T) {
	t.Setenv("GT_GATE_GOCACHE_BASE", t.TempDir())
	t.Setenv("GT_GATE_TMPDIR", "off")
	t.Setenv("BEADS_DOLT_PORT", "3307")
	t.Setenv("GT_DOLT_PORT", "3307")

	e := &Engineer{} // no rig → no scoping
	env := e.gateBuildEnv()
	if env == nil {
		t.Fatal("gateBuildEnv returned nil, want scrubbed env")
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "BEADS_DOLT_") || strings.HasPrefix(kv, "GT_DOLT_") {
			t.Errorf("gateBuildEnv leaked Dolt-routing var %q to gate subprocess", kv)
		}
	}
}

// gu-l4aue: even an unscoped engineer (no rig → no GOCACHE scoping) still gets
// TMPDIR/GOTMPDIR routed off tmpfs, so concurrent full-suite gate runs can't
// fill a small /tmp and fail the linker with ENOSPC.
func TestGateBuildEnv_TmpdirOverrideWhenUnscoped(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GT_GATE_TMPDIR_BASE", base)

	e := &Engineer{} // no rig
	env := e.gateBuildEnv()
	if env == nil {
		t.Fatal("gateBuildEnv returned nil, want env with TMPDIR override")
	}

	wantDir := filepath.Join(base, "gt-gate-tmp")
	var sawTmp, sawGoTmp bool
	for _, kv := range env {
		if kv == "TMPDIR="+wantDir {
			sawTmp = true
		}
		if kv == "GOTMPDIR="+wantDir {
			sawGoTmp = true
		}
	}
	if !sawTmp || !sawGoTmp {
		t.Errorf("gateBuildEnv missing TMPDIR/GOTMPDIR=%q (TMPDIR=%v GOTMPDIR=%v)", wantDir, sawTmp, sawGoTmp)
	}
}
