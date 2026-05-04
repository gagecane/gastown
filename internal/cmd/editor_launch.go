package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/steveyegge/gastown/internal/util"
)

// launchEditor opens path in $EDITOR (default: vi). In agent context
// (GT_ROLE set), it refuses with a helpful error instead of dropping into
// an interactive editor that the agent has no way to exit — see gu-pkf3
// / gt-ube24 category A for production failures this guards against.
//
// commandName is the user-facing `gt ...` command (e.g. "gt hooks base")
// used in the refusal hint. suggestion is a one-line tip like
// "Use `gt hooks base --show` to inspect the current config." If empty,
// a generic hint is printed.
func launchEditor(path, commandName, suggestion string) error {
	if util.IsAgentContext() {
		msg := fmt.Sprintf("%s requires an interactive terminal; refusing to launch $EDITOR under GT_ROLE=%q (would hang the agent session).",
			commandName, os.Getenv("GT_ROLE"))
		if suggestion != "" {
			msg += "\nHint: " + suggestion
		} else {
			msg += fmt.Sprintf("\nHint: edit the file directly: %s", path)
		}
		return fmt.Errorf("%s", msg)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	editorCmd := exec.Command(editor, path)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr
	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("running editor: %w", err)
	}
	return nil
}
