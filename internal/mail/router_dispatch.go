package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// sendToGroup resolves a @group address and sends individual messages to each member.
func (r *Router) sendToGroup(msg *Message) error {
	group := parseGroupAddress(msg.To)
	if group == nil {
		return fmt.Errorf("invalid group address: %s", msg.To)
	}

	recipients, err := r.resolveGroup(group)
	if err != nil {
		return fmt.Errorf("resolving group %s: %w", msg.To, err)
	}

	if len(recipients) == 0 {
		return fmt.Errorf("no recipients found for group: %s", msg.To)
	}

	// Fan-out: send a copy to each recipient
	var errs []string
	for _, recipient := range recipients {
		// Create a copy of the message for this recipient
		msgCopy := *msg
		msgCopy.To = recipient
		msgCopy.ID = "" // Each fan-out copy gets its own ID from bd create

		if err := r.sendToSingle(&msgCopy); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", recipient, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("some group sends failed: %s", strings.Join(errs, "; "))
	}

	return nil
}

// sendToSingle sends a message to a single recipient.
func (r *Router) sendToSingle(msg *Message) error {
	// Ensure message has an ID for in-memory tracking (notifications, logging).
	// We no longer pass --id to bd create; bd auto-generates the correct prefix.
	if msg.ID == "" {
		msg.ID = GenerateID()
	}

	// Validate message before sending
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("invalid message: %w", err)
	}

	// Convert addresses to beads identities
	toIdentity := AddressToIdentity(msg.To)
	// Expand crew/polecats shorthand (e.g., "crew/bob" → "pata/bob")
	toIdentity = r.resolveCrewShorthand(toIdentity)

	// Validate recipient exists
	if err := r.validateRecipient(toIdentity); err != nil {
		return fmt.Errorf("invalid recipient %q: %w", msg.To, err)
	}

	// Build labels for type, from/thread/reply-to/cc
	labels := r.buildLabels(msg)

	// Build command: bd create --assignee=<recipient> -d <body> --labels=gt:message,... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags (see web/api.go).
	// Let bd auto-generate the ID with the correct database prefix.
	args := []string{"create",
		"--assignee", toIdentity,
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Do NOT pass --id to bd create. The msg.ID (msg-xxx prefix) is for
	// in-memory tracking only. bd auto-generates IDs with the correct
	// database prefix (e.g., hq-wisp-xxx). Passing --id causes prefix
	// mismatch errors when the msg- prefix does not match the database.

	// Add --ephemeral flag for ephemeral messages (wisps, not synced to git)
	if r.shouldBeWisp(msg) {
		args = append(args, "--ephemeral")
	}

	// End flag parsing with --, then add subject as positional argument.
	// This prevents subjects like "--help" or "--json" from being parsed as flags.
	args = appendMetadataArgs(args, msg)
	args = append(args, "--", msg.Subject)

	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	telemetry.RecordMailMessage(context.Background(), "send", telemetry.MailMessageInfo{
		ID:       msg.ID,
		From:     msg.From,
		To:       msg.To,
		Subject:  msg.Subject,
		Body:     msg.Body,
		ThreadID: msg.ThreadID,
		Priority: string(msg.Priority),
		MsgType:  string(msg.Type),
	}, err)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}

	// Notify recipient if they have an active session (best-effort notification).
	// Skip when the caller explicitly suppressed notification (--no-notify)
	// or for self-mail (handoffs to future-self don't need present-self notified).
	// Notification is async: the durable write is complete, so the caller
	// doesn't block on idle probing (up to 1s per recipient in fan-out).
	// Callers that exit soon after Send should call WaitPendingNotifications.
	if !msg.SuppressNotify && !isSelfMail(msg.From, msg.To) {
		msgCopy := *msg // copy to avoid data race if caller mutates msg
		r.notifyWg.Add(1)
		go func() {
			defer r.notifyWg.Done()
			r.notifyRecipient(&msgCopy) //nolint:errcheck
		}()
	}

	return nil
}

// sendToList expands a mailing list and sends individual copies to each recipient.
// Each recipient gets their own message copy with the same content.
// Collects all delivery errors and reports partial failures.
func (r *Router) sendToList(msg *Message) error {
	listName := parseListName(msg.To)
	recipients, err := r.expandList(listName)
	if err != nil {
		return err
	}

	// Fan-out: send a copy to each recipient, collecting all errors
	var errs []string
	for _, recipient := range recipients {
		// Create a copy of the message for this recipient
		msgCopy := *msg
		msgCopy.To = recipient
		msgCopy.ID = "" // Each fan-out copy gets its own ID from bd create

		if err := r.Send(&msgCopy); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", recipient, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sending to list %s: some deliveries failed: %s", listName, strings.Join(errs, "; "))
	}

	return nil
}

// sendToQueue delivers a message to a queue for worker claiming.
// Unlike sendToList, this creates a SINGLE message (no fan-out).
// The message is stored in town-level beads with queue metadata.
// Workers claim messages using bd update --claimed-by.
func (r *Router) sendToQueue(msg *Message) error {
	queueName := parseQueueName(msg.To)

	// Validate queue exists in messaging config
	_, err := r.expandQueue(queueName)
	if err != nil {
		return err
	}

	// Build labels for type, from/thread/reply-to/cc plus queue metadata
	var labels []string
	labels = append(labels, "gt:message")
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "queue:"+queueName)
	labels = append(labels, DeliverySendLabels()...)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}

	// Build command: bd create --assignee=queue:<name> -d <body> ... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	// Use queue:<name> as assignee so inbox queries can filter by queue
	args := []string{"create",
		"--assignee", msg.To, // queue:name
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels (includes queue name for filtering)
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Queue messages are never ephemeral - they need to persist until claimed
	// (deliberately not checking shouldBeWisp)

	// End flag parsing, then subject as positional argument
	args = appendMetadataArgs(args, msg)
	args = append(args, "--", msg.Subject)

	// Queue messages go to town-level beads (shared location)
	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err = runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("sending to queue %s: %w", queueName, err)
	}

	// No notification for queue messages - workers poll or check on their own schedule

	return nil
}

// sendToAnnounce delivers a message to an announce channel (bulletin board).
// Unlike sendToQueue, no claiming is supported - messages persist until retention limit.
// ONE copy is stored in town-level beads with announce_channel metadata.
func (r *Router) sendToAnnounce(msg *Message) error {
	announceName := parseAnnounceName(msg.To)

	// Validate announce channel exists and get config
	announceCfg, err := r.expandAnnounce(announceName)
	if err != nil {
		return err
	}

	// Apply retention pruning BEFORE creating new message
	if announceCfg.RetainCount > 0 {
		if err := r.pruneAnnounce(announceName, announceCfg.RetainCount); err != nil {
			// Log but don't fail - pruning is best-effort
			// The new message should still be created
			_ = err
		}
	}

	// Build labels for type, from/thread/reply-to/cc plus announce metadata.
	// Note: delivery:pending is intentionally omitted for announce messages —
	// broadcast messages have no single recipient to ack against. Subscriber
	// fan-out copies go through sendToSingle which adds delivery tracking.
	var labels []string
	labels = append(labels, "gt:message")
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "announce:"+announceName)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}

	// Build command: bd create --assignee=announce:<name> -d <body> ... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	// Use announce:<name> as assignee so queries can filter by channel
	args := []string{"create",
		"--assignee", msg.To, // announce:name
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels (includes announce name for filtering)
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Announce messages are never ephemeral - they need to persist for readers
	// (deliberately not checking shouldBeWisp)

	// End flag parsing, then subject as positional argument
	args = appendMetadataArgs(args, msg)
	args = append(args, "--", msg.Subject)

	// Announce messages go to town-level beads (shared location)
	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err = runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("sending to announce %s: %w", announceName, err)
	}

	// No notification for announce messages - readers poll or check on their own schedule

	return nil
}

// sendToChannel delivers a message to a beads-native channel.
// Creates a message with channel:<name> label for channel queries.
// Also fans out delivery to each subscriber's inbox.
// Retention is enforced by the channel's EnforceChannelRetention after message creation.
func (r *Router) sendToChannel(msg *Message) error {
	channelName := parseChannelName(msg.To)

	// Validate channel exists as a beads-native channel
	if r.townRoot == "" {
		return fmt.Errorf("town root not set, cannot send to channel: %s", channelName)
	}
	b := beads.New(r.townRoot)
	_, fields, err := b.GetChannelBead(channelName)
	if err != nil {
		return fmt.Errorf("getting channel %s: %w", channelName, err)
	}
	if fields == nil {
		return fmt.Errorf("channel not found: %s", channelName)
	}
	if fields.Status == beads.ChannelStatusClosed {
		return fmt.Errorf("channel %s is closed", channelName)
	}

	// Build labels for type, from/thread/reply-to/cc plus channel metadata.
	// Note: delivery:pending is intentionally omitted for the channel-origin
	// copy — it has no single recipient to ack. Subscriber fan-out copies go
	// through sendToSingle which adds delivery tracking.
	var labels []string
	labels = append(labels, "gt:message")
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "channel:"+channelName)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}

	// Build command: bd create --assignee=channel:<name> -d <body> ... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	// Use channel:<name> as assignee so queries can filter by channel
	args := []string{"create",
		"--assignee", msg.To, // channel:name
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels (includes channel name for filtering)
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Channel messages are never ephemeral - they persist according to retention policy
	// (deliberately not checking shouldBeWisp)

	// End flag parsing, then subject as positional argument
	args = appendMetadataArgs(args, msg)
	args = append(args, "--", msg.Subject)

	// Channel messages go to town-level beads (shared location)
	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err = runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("sending to channel %s: %w", channelName, err)
	}

	// Enforce channel retention policy (on-write cleanup)
	_ = b.EnforceChannelRetention(channelName)

	// Fan-out delivery: send a copy to each subscriber's inbox
	if len(fields.Subscribers) > 0 {
		var errs []string
		for _, subscriber := range fields.Subscribers {
			// Skip self-delivery (don't notify the sender)
			if isSelfMail(msg.From, subscriber) {
				continue
			}

			// Create a copy for this subscriber with channel context in subject
			msgCopy := *msg
			msgCopy.To = subscriber
			msgCopy.ID = "" // Each fan-out copy gets its own ID from bd create
			msgCopy.Subject = fmt.Sprintf("[channel:%s] %s", channelName, msg.Subject)

			if err := r.sendToSingle(&msgCopy); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", subscriber, err))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("channel %s: some subscriber deliveries failed: %s", channelName, strings.Join(errs, "; "))
		}
	}

	return nil
}

// pruneAnnounce deletes oldest messages from an announce channel to enforce retention.
// If the channel has >= retainCount messages, deletes the oldest until count < retainCount.
func (r *Router) pruneAnnounce(announceName string, retainCount int) error {
	if retainCount <= 0 {
		return nil // No retention limit
	}

	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}

	// Query existing messages in this announce channel
	// Use bd list with labels filter to find messages with gt:message and announce:<name> labels
	args := []string{"list",
		"--labels=gt:message,announce:" + announceName,
		"--json",
		"--limit=0", // Get all
		"--sort=created",
		"--asc", // Oldest first
	}

	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("querying announce messages: %w", err)
	}

	// Parse message list
	var messages []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout, &messages); err != nil {
		return fmt.Errorf("parsing announce messages: %w", err)
	}

	// Calculate how many to delete (we're about to add 1 more)
	// If we have N messages and retainCount is R, we need to keep at most R-1 after pruning
	// so the new message makes it exactly R
	toDelete := len(messages) - (retainCount - 1)
	if toDelete <= 0 {
		return nil // No pruning needed
	}

	// Delete oldest messages
	for i := 0; i < toDelete && i < len(messages); i++ {
		deleteArgs := []string{"close", messages[i].ID, "--reason=retention pruning"}
		// Best-effort deletion - don't fail if one delete fails
		delCtx, delCancel := bdWriteCtx()
		_, _ = runBdCommand(delCtx, deleteArgs, filepath.Dir(beadsDir), beadsDir)
		delCancel()
	}

	return nil
}

// GetMailbox returns a Mailbox for the given address.
// Routes to the correct beads database based on the address.
func (r *Router) GetMailbox(address string) (*Mailbox, error) {
	beadsDir := r.resolveBeadsDir()
	workDir := filepath.Dir(beadsDir) // Parent of .beads
	return NewMailboxFromAddress(address, workDir), nil
}
