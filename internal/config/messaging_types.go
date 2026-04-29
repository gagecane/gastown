// Messaging (mailing lists, work queues, announcements) configuration types.
// Extracted from types.go.
package config

// MessagingConfig represents the messaging configuration (config/messaging.json).
// This defines mailing lists, work queues, and announcement channels.
type MessagingConfig struct {
	Type    string `json:"type"`    // "messaging"
	Version int    `json:"version"` // schema version

	// Lists are static mailing lists. Messages are fanned out to all recipients.
	// Each recipient gets their own copy of the message.
	// Example: {"oncall": ["mayor/", "gastown/witness"]}
	Lists map[string][]string `json:"lists,omitempty"`

	// Queues are shared work queues. Only one copy exists; workers claim messages.
	// Messages sit in the queue until explicitly claimed by a worker.
	// Example: {"work/gastown": ["gastown/polecats/*"]}
	Queues map[string]QueueConfig `json:"queues,omitempty"`

	// Announces are bulletin boards. One copy exists; anyone can read, no claiming.
	// Used for broadcast announcements that don't need acknowledgment.
	// Example: {"alerts": {"readers": ["@town"]}}
	Announces map[string]AnnounceConfig `json:"announces,omitempty"`

	// NudgeChannels are named groups for real-time nudge fan-out.
	// Like mailing lists but for tmux send-keys instead of durable mail.
	// Example: {"workers": ["gastown/polecats/*", "gastown/crew/*"], "witnesses": ["*/witness"]}
	NudgeChannels map[string][]string `json:"nudge_channels,omitempty"`
}

// QueueConfig represents a work queue configuration.
type QueueConfig struct {
	// Workers lists addresses eligible to claim from this queue.
	// Supports wildcards: "gastown/polecats/*" matches all polecats in gastown.
	Workers []string `json:"workers"`

	// MaxClaims is the maximum number of concurrent claims (0 = unlimited).
	MaxClaims int `json:"max_claims,omitempty"`
}

// AnnounceConfig represents a bulletin board configuration.
type AnnounceConfig struct {
	// Readers lists addresses eligible to read from this announce channel.
	// Supports @group syntax: "@town", "@rig/gastown", "@witnesses".
	Readers []string `json:"readers"`

	// RetainCount is the number of messages to retain (0 = unlimited).
	RetainCount int `json:"retain_count,omitempty"`
}

// CurrentMessagingVersion is the current schema version for MessagingConfig.
const CurrentMessagingVersion = 1

// NewMessagingConfig creates a new MessagingConfig with defaults.
func NewMessagingConfig() *MessagingConfig {
	return &MessagingConfig{
		Type:          "messaging",
		Version:       CurrentMessagingVersion,
		Lists:         make(map[string][]string),
		Queues:        make(map[string]QueueConfig),
		Announces:     make(map[string]AnnounceConfig),
		NudgeChannels: make(map[string][]string),
	}
}
