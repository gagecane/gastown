package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/util"
)

// dogDoneSubjectPrefix is the subject prefix dogs use when signaling
// completion via mail. Dog formulas send mail with subject "DOG_DONE ..."
// or "DOG_DONE: ...". See gu-7537 for details.
const dogDoneSubjectPrefix = "DOG_DONE"

// dogSenderPrefix is the prefix of the From address on mail sent by a dog.
// Dog identities have the form "deacon/dogs/<name>".
const dogSenderPrefix = "deacon/dogs/"

// extractDogNameFromSender returns the dog name when from is a dog sender
// identity (deacon/dogs/<name>), and empty string otherwise.
//
// Exported as a pure helper so tests can validate the parsing without
// subprocess setup.
func extractDogNameFromSender(from string) string {
	if !strings.HasPrefix(from, dogSenderPrefix) {
		return ""
	}
	name := strings.TrimPrefix(from, dogSenderPrefix)
	// Strip any sub-path (defense in depth: identities should be flat, but
	// never trust input).
	if slash := strings.IndexByte(name, '/'); slash >= 0 {
		name = name[:slash]
	}
	return strings.TrimSpace(name)
}

// isDogDoneMessage reports whether the given subject is a DOG_DONE completion
// signal. Dogs use two variants: "DOG_DONE <hostname>" (space-separated) and
// "DOG_DONE: <task>" (colon-separated). Both are accepted.
func isDogDoneMessage(subject string) bool {
	trimmed := strings.TrimSpace(subject)
	if trimmed == dogDoneSubjectPrefix {
		// Bare "DOG_DONE" is also accepted — the dog name comes from From.
		return true
	}
	// Require DOG_DONE followed by a delimiter so we don't match a subject
	// like "DOG_DONEISH" by accident.
	if !strings.HasPrefix(trimmed, dogDoneSubjectPrefix) {
		return false
	}
	next := trimmed[len(dogDoneSubjectPrefix)]
	return next == ' ' || next == ':' || next == '\t'
}

// processDogDoneMessages scans the deacon inbox for DOG_DONE mail from dogs
// and clears each sending dog's work assignment back to idle.
//
// Without this handler, dogs that finish infrastructure tasks (mol-orphan-scan,
// mol-dog-reaper, etc.) stay pinned in state=working forever because the dog
// formulas only send completion mail — they don't call `gt dog done`. The
// deacon's patrol eventually force-clears stuck dogs after 98m via
// stuck-dog-check, but that leaks dog slots and blocks new dispatch. See
// gu-7537.
//
// The handler is a no-op when:
//   - the deacon has no mail
//   - there are no DOG_DONE messages
//   - the sender is not a dog (msg.From does not start with deacon/dogs/)
//   - the named dog no longer exists in the kennel
//
// Each processed message is deleted so it is not reprocessed on the next
// heartbeat, mirroring ProcessLifecycleRequests. The deacon patrol formula
// also archives DOG_DONE mail from its own inbox view; both sides converge
// on an empty inbox.
func (d *Daemon) processDogDoneMessages(mgr *dog.Manager) {
	messages, err := d.fetchDeaconInbox()
	if err != nil {
		d.logger.Printf("DogDone: failed to fetch deacon inbox: %v", err)
		return
	}
	if len(messages) == 0 {
		return
	}

	d.processDogDoneMessageList(mgr, messages, d.closeMessage)
}

// processDogDoneMessageList is the inner loop of processDogDoneMessages,
// factored out so tests can drive it with synthetic messages and a mock
// deleter without needing a real `gt mail` subprocess.
//
// deleteMessage is called to remove each handled DOG_DONE message from the
// deacon inbox. Pass nil to skip deletion (tests that only care about state
// transitions).
func (d *Daemon) processDogDoneMessageList(
	mgr *dog.Manager,
	messages []BeadsMessage,
	deleteMessage func(id string) error,
) {
	for _, msg := range messages {
		if msg.Read {
			continue
		}
		if !isDogDoneMessage(msg.Subject) {
			continue
		}

		dogName := extractDogNameFromSender(msg.From)
		if dogName == "" {
			// Not from a dog — ignore (could be a test or misrouted mail).
			d.logger.Printf("DogDone: ignoring DOG_DONE from non-dog sender %q", msg.From)
			continue
		}

		// Delete the mail first. Same "claim then execute" pattern used by
		// ProcessLifecycleRequests: even if the ClearWork call below fails,
		// the message is gone and won't be reprocessed on every heartbeat.
		// The dog's last_active will still be stale, so detectStaleWorkingDogs
		// remains a safety net for the rare failure case.
		if deleteMessage != nil {
			if err := deleteMessage(msg.ID); err != nil {
				d.logger.Printf("DogDone: warning: failed to delete message %s: %v", msg.ID, err)
				// Continue anyway — still attempt the state transition.
			}
		}

		dg, err := mgr.Get(dogName)
		if err != nil {
			// Dog not found: might have been removed from the kennel before
			// we got to its mail. Nothing to do.
			d.logger.Printf("DogDone: dog %q not found (from=%s), skipping", dogName, msg.From)
			continue
		}

		if dg.State == dog.StateIdle && dg.Work == "" {
			// Already idle — nothing to clear, but log so operators can see
			// DOG_DONE mail is being processed.
			d.logger.Printf("DogDone: dog %s already idle, mail %s processed", dogName, msg.ID)
			continue
		}

		if err := mgr.ClearWork(dogName); err != nil {
			d.logger.Printf("DogDone: failed to clear work for dog %s: %v", dogName, err)
			continue
		}
		d.logger.Printf("DogDone: cleared dog %s to idle (was: %s)", dogName, dg.Work)
	}
}

// fetchDeaconInbox returns all messages in the deacon's inbox. This is a
// wrapper around `gt mail inbox --identity deacon/ --json` that mirrors the
// implementation in ProcessLifecycleRequests, factored out so the dog-done
// handler can reuse it.
func (d *Daemon) fetchDeaconInbox() ([]BeadsMessage, error) {
	cmd := exec.Command(d.gtPath, "mail", "inbox", "--identity", "deacon/", "--json")
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	if len(output) == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}

	var messages []BeadsMessage
	if err := json.Unmarshal(output, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}
