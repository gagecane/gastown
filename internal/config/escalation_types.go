// Escalation configuration types and severity helpers. Extracted from types.go.
package config

// EscalationConfig represents escalation routing configuration (settings/escalation.json).
// This defines severity-based routing for escalations to different channels.
type EscalationConfig struct {
	Type    string `json:"type"`    // "escalation"
	Version int    `json:"version"` // schema version

	// Routes maps severity levels to action lists.
	// Actions are executed in order for each escalation.
	// Action formats:
	//   - "bead"        → Create escalation bead (always first, implicit)
	//   - "mail:<target>" → Send gt mail to target (e.g., "mail:mayor")
	//   - "email:human" → Send email to contacts.human_email
	//   - "sms:human"   → Send SMS to contacts.human_sms
	//   - "slack"       → Post to contacts.slack_webhook
	//   - "log"         → Write to escalation log file
	Routes map[string][]string `json:"routes"`

	// Contacts contains contact information for external notification actions.
	Contacts EscalationContacts `json:"contacts"`

	// StaleThreshold is how long before an unacknowledged escalation
	// is considered stale and gets re-escalated.
	// Format: Go duration string (e.g., "4h", "30m", "24h")
	// Default: "4h"
	StaleThreshold string `json:"stale_threshold,omitempty"`

	// MaxReescalations limits how many times an escalation can be
	// re-escalated. Default: 2 (low→medium→high, then stops)
	// Pointer type to distinguish "not configured" (nil) from explicit 0.
	MaxReescalations *int `json:"max_reescalations,omitempty"`
}

// EscalationContacts contains contact information for external notification channels.
type EscalationContacts struct {
	HumanEmail   string `json:"human_email,omitempty"`   // email address for email:human action
	HumanSMS     string `json:"human_sms,omitempty"`     // phone number for sms:human action
	SlackWebhook string `json:"slack_webhook,omitempty"` // webhook URL for slack action
	SlackScript  string `json:"slack_script,omitempty"`  // path to script for slack action (takes message as $1)
	SMTPHost     string `json:"smtp_host,omitempty"`     // SMTP server host (e.g. "smtp.gmail.com")
	SMTPPort     string `json:"smtp_port,omitempty"`     // SMTP server port (default "587")
	SMTPFrom     string `json:"smtp_from,omitempty"`     // sender address for email notifications
	SMTPUser     string `json:"smtp_user,omitempty"`     // SMTP auth username (optional)
	SMTPPass     string `json:"smtp_pass,omitempty"`     // SMTP auth password (optional)
	SMSWebhook   string `json:"sms_webhook,omitempty"`   // webhook URL for SMS delivery (e.g. Twilio)
}

// CurrentEscalationVersion is the current schema version for EscalationConfig.
const CurrentEscalationVersion = 1

// Escalation severity level constants.
const (
	SeverityCritical = "critical" // P0: immediate attention required
	SeverityHigh     = "high"     // P1: urgent, needs attention soon
	SeverityMedium   = "medium"   // P2: standard escalation (default)
	SeverityLow      = "low"      // P3: informational, can wait
)

// ValidSeverities returns the list of valid severity levels in order of priority.
func ValidSeverities() []string {
	return []string{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}
}

// IsValidSeverity checks if a severity level is valid.
func IsValidSeverity(severity string) bool {
	switch severity {
	case SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
		return true
	default:
		return false
	}
}

// NextSeverity returns the next higher severity level for re-escalation.
// Returns the same level if already at critical.
func NextSeverity(severity string) string {
	switch severity {
	case SeverityLow:
		return SeverityMedium
	case SeverityMedium:
		return SeverityHigh
	case SeverityHigh:
		return SeverityCritical
	default:
		return SeverityCritical
	}
}

// NewEscalationConfig creates a new EscalationConfig with sensible defaults.
func NewEscalationConfig() *EscalationConfig {
	return &EscalationConfig{
		Type:    "escalation",
		Version: CurrentEscalationVersion,
		Routes: map[string][]string{
			SeverityLow:      {"bead"},
			SeverityMedium:   {"bead", "mail:mayor"},
			SeverityHigh:     {"bead", "mail:mayor", "email:human"},
			SeverityCritical: {"bead", "mail:mayor", "email:human", "sms:human"},
		},
		Contacts:         EscalationContacts{},
		StaleThreshold:   "4h",
		MaxReescalations: intPtr(2),
	}
}
