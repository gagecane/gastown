package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// helper to create a plugin directory with a plugin.md and optional extra files.
func createTestPlugin(t *testing.T, dir, name, content string, extras map[string]string) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for fname, fcontent := range extras {
		if err := os.WriteFile(filepath.Join(pluginDir, fname), []byte(fcontent), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncPlugins_CopiesNew(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "my-plugin", "+++\nname = \"my-plugin\"\n+++\ndo stuff", nil)

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 1 || result.Copied[0] != "my-plugin" {
		t.Errorf("expected 1 copied plugin, got %v", result.Copied)
	}

	// Verify file exists at target
	if _, err := os.Stat(filepath.Join(dstDir, "my-plugin", "plugin.md")); err != nil {
		t.Errorf("plugin.md not copied: %v", err)
	}
}

func TestSyncPlugins_SkipsUpToDate(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	content := "+++\nname = \"my-plugin\"\n+++\ndo stuff"
	createTestPlugin(t, srcDir, "my-plugin", content, nil)
	createTestPlugin(t, dstDir, "my-plugin", content, nil)

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped plugin, got %v", result.Skipped)
	}
	if len(result.Copied) != 0 {
		t.Errorf("expected 0 copied, got %v", result.Copied)
	}
}

func TestSyncPlugins_UpdatesChanged(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "my-plugin", "+++\nname = \"my-plugin\"\n+++\nv2 instructions", nil)
	createTestPlugin(t, dstDir, "my-plugin", "+++\nname = \"my-plugin\"\n+++\nv1 instructions", nil)

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 1 {
		t.Errorf("expected 1 copied plugin, got %v", result.Copied)
	}

	// Verify target has new content
	data, err := os.ReadFile(filepath.Join(dstDir, "my-plugin", "plugin.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "+++\nname = \"my-plugin\"\n+++\nv2 instructions" {
		t.Errorf("content not updated: %s", data)
	}
}

func TestSyncPlugins_CopiesExtraFiles(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "my-plugin", "+++\nname = \"my-plugin\"\n+++\nstuff",
		map[string]string{"run.sh": "#!/bin/bash\necho hi"})

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 1 {
		t.Errorf("expected 1 copied, got %v", result.Copied)
	}

	// Verify run.sh was copied
	data, err := os.ReadFile(filepath.Join(dstDir, "my-plugin", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/bash\necho hi" {
		t.Errorf("run.sh content wrong: %s", data)
	}

	// Verify executable permission preserved (skip on Windows where permission bits aren't meaningful)
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dstDir, "my-plugin", "run.sh"))
		if info.Mode()&0111 == 0 {
			t.Error("run.sh lost executable permission")
		}
	}
}

func TestSyncPlugins_CleanRemovesExtra(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "keep-me", "+++\nname = \"keep-me\"\n+++\nkeep", nil)
	createTestPlugin(t, dstDir, "keep-me", "+++\nname = \"keep-me\"\n+++\nkeep", nil)
	createTestPlugin(t, dstDir, "old-plugin", "+++\nname = \"old-plugin\"\n+++\nold", nil)

	result, err := SyncPlugins(srcDir, dstDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "old-plugin" {
		t.Errorf("expected old-plugin removed, got %v", result.Removed)
	}

	// Verify old plugin was removed
	if _, err := os.Stat(filepath.Join(dstDir, "old-plugin")); !os.IsNotExist(err) {
		t.Error("old-plugin should have been removed")
	}
}

func TestSyncPlugins_NoCleanKeepsExtra(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "new-plugin", "+++\nname = \"new-plugin\"\n+++\nnew", nil)
	createTestPlugin(t, dstDir, "old-plugin", "+++\nname = \"old-plugin\"\n+++\nold", nil)

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 {
		t.Errorf("expected 0 removed (clean=false), got %v", result.Removed)
	}

	// Verify old plugin still exists
	if _, err := os.Stat(filepath.Join(dstDir, "old-plugin", "plugin.md")); err != nil {
		t.Error("old-plugin should still exist when clean=false")
	}
}

func TestSyncPlugins_IgnoresNonPluginDirs(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create a directory without plugin.md — should be ignored
	notPlugin := filepath.Join(srcDir, "not-a-plugin")
	if err := os.MkdirAll(notPlugin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notPlugin, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 0 {
		t.Errorf("expected 0 copied (no valid plugins), got %v", result.Copied)
	}
}

func TestSyncPlugins_SkipsDisabled(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "foreign", "+++\nname = \"foreign\"\n+++\nrun foreign", nil)
	// Plugin is parked in <target>/.disabled/ — a manual disable.
	createTestPlugin(t, filepath.Join(dstDir, ".disabled"), "foreign",
		"+++\nname = \"foreign\"\n+++\nrun foreign", nil)

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Disabled) != 1 || result.Disabled[0] != "foreign" {
		t.Errorf("expected foreign reported disabled, got %v", result.Disabled)
	}
	if len(result.Copied) != 0 {
		t.Errorf("expected 0 copied (plugin disabled), got %v", result.Copied)
	}
	// The disable must stick: no active copy should be created.
	if _, err := os.Stat(filepath.Join(dstDir, "foreign")); !os.IsNotExist(err) {
		t.Error("disabled plugin should not be re-copied into active runtime")
	}
}

func TestSyncPlugins_DisabledRemovesActiveCopy(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	content := "+++\nname = \"foreign\"\n+++\nrun foreign"
	createTestPlugin(t, srcDir, "foreign", content, nil)
	// An active copy from a prior sync, now also disabled.
	createTestPlugin(t, dstDir, "foreign", content, nil)
	createTestPlugin(t, filepath.Join(dstDir, ".disabled"), "foreign", content, nil)

	result, err := SyncPlugins(srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Disabled) != 1 {
		t.Errorf("expected foreign disabled, got %v", result.Disabled)
	}
	// Sync must remove the stale active copy so the disable takes effect.
	if _, err := os.Stat(filepath.Join(dstDir, "foreign")); !os.IsNotExist(err) {
		t.Error("active copy of disabled plugin should be removed")
	}
}

func TestDetectDrift_IgnoresDisabled(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "foreign", "+++\nname = \"foreign\"\n+++\nrun", nil)
	createTestPlugin(t, filepath.Join(dstDir, ".disabled"), "foreign",
		"+++\nname = \"foreign\"\n+++\nrun", nil)

	report, err := DetectDrift(srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	// A disabled plugin is intentionally absent — not drift, not missing.
	if report.HasDrift() {
		t.Errorf("disabled plugin should not register as drift: %+v", report)
	}
	if len(report.Missing) != 0 {
		t.Errorf("disabled plugin should not be reported missing, got %v", report.Missing)
	}
}

func TestDetectDrift_NoDrift(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	content := "+++\nname = \"stable\"\n+++\nstuff"
	createTestPlugin(t, srcDir, "stable", content, nil)
	createTestPlugin(t, dstDir, "stable", content, nil)

	report, err := DetectDrift(srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if report.HasDrift() {
		t.Error("expected no drift")
	}
}

func TestDetectDrift_ContentDiffers(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "changed", "+++\nname = \"changed\"\n+++\nv2", nil)
	createTestPlugin(t, dstDir, "changed", "+++\nname = \"changed\"\n+++\nv1", nil)

	report, err := DetectDrift(srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasDrift() {
		t.Error("expected drift")
	}
	if len(report.Drifted) != 1 || report.Drifted[0].Name != "changed" {
		t.Errorf("expected changed in drifted, got %v", report.Drifted)
	}
}

func TestDetectDrift_MissingFromTarget(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, srcDir, "new-one", "+++\nname = \"new-one\"\n+++\nnew", nil)

	report, err := DetectDrift(srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasDrift() {
		t.Error("expected drift")
	}
	if len(report.Missing) != 1 || report.Missing[0] != "new-one" {
		t.Errorf("expected new-one missing, got %v", report.Missing)
	}
}

func TestDetectDrift_ExtraInTarget(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	createTestPlugin(t, dstDir, "orphan", "+++\nname = \"orphan\"\n+++\nold", nil)

	report, err := DetectDrift(srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	// Extra plugins are not drift (no HasDrift), but are reported
	if len(report.Extra) != 1 || report.Extra[0] != "orphan" {
		t.Errorf("expected orphan in extra, got %v", report.Extra)
	}
}
