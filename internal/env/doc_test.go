package env

import (
	"strings"
	"testing"
)

func TestMarkdown(t *testing.T) {
	out := Markdown()
	if !strings.Contains(out, "| Variable | Kind | Default | Description |") {
		t.Error("Markdown() missing header row")
	}
	if !strings.Contains(out, "`GT_DOLT_PORT`") {
		t.Error("Markdown() missing GT_DOLT_PORT")
	}
	if !strings.Contains(out, "`GT_ROLE`") {
		t.Error("Markdown() missing GT_ROLE")
	}
	// The "—" em dash should appear for defaults that are empty.
	if !strings.Contains(out, "—") {
		t.Error("Markdown() missing em-dash placeholder for empty defaults")
	}
}
