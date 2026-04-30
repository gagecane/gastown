package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
)

// deliveryStatus captures the outcome of notifying a single escalation channel.
type deliveryStatus struct {
	Target            string `json:"target,omitempty"`
	Channel           string `json:"channel"`
	Created           bool   `json:"created,omitempty"`
	Persisted         bool   `json:"persisted,omitempty"`
	RuntimeNotified   bool   `json:"runtime_notified,omitempty"`
	Annotated         bool   `json:"annotated,omitempty"`
	Severity          string `json:"severity,omitempty"`
	Error             string `json:"error,omitempty"`
	Warning           string `json:"warning,omitempty"`
	NotificationRoute string `json:"notification_route,omitempty"`
}

// extractMailTargetsFromActions extracts mail targets from action strings.
// Action format: "mail:target" returns "target"
// E.g., ["bead", "mail:mayor", "email:human"] returns ["mayor"]
func extractMailTargetsFromActions(actions []string) []string {
	var targets []string
	for _, action := range actions {
		if strings.HasPrefix(action, "mail:") {
			target := strings.TrimPrefix(action, "mail:")
			if target != "" {
				targets = append(targets, target)
			}
		}
	}
	return targets
}

// executeExternalActions processes external notification actions (email:, sms:, slack, log).
func executeExternalActions(actions []string, cfg *config.EscalationConfig, beadID, severity, description, townRoot string) []deliveryStatus {
	statuses := []deliveryStatus{}
	for _, action := range actions {
		switch {
		case strings.HasPrefix(action, "email:"):
			status := deliveryStatus{Channel: "email", Target: strings.TrimPrefix(action, "email:"), Severity: severity}
			if cfg.Contacts.HumanEmail == "" {
				status.Warning = "contacts.human_email not configured"
				style.PrintWarning("email action '%s' skipped: contacts.human_email not configured in settings/escalation.json", action)
			} else if cfg.Contacts.SMTPHost == "" {
				status.Warning = "contacts.smtp_host not configured"
				style.PrintWarning("email action '%s' skipped: contacts.smtp_host not configured in settings/escalation.json", action)
			} else {
				if err := sendEscalationEmail(cfg, beadID, severity, description); err != nil {
					status.Error = err.Error()
					style.PrintWarning("email send failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  📧 Email sent to %s\n", cfg.Contacts.HumanEmail)
				}
			}
			statuses = append(statuses, status)

		case strings.HasPrefix(action, "sms:"):
			status := deliveryStatus{Channel: "sms", Target: strings.TrimPrefix(action, "sms:"), Severity: severity}
			if cfg.Contacts.HumanSMS == "" {
				status.Warning = "contacts.human_sms not configured"
				style.PrintWarning("sms action '%s' skipped: contacts.human_sms not configured in settings/escalation.json", action)
			} else if cfg.Contacts.SMSWebhook == "" {
				status.Warning = "contacts.sms_webhook not configured"
				style.PrintWarning("sms action '%s' skipped: contacts.sms_webhook not configured in settings/escalation.json", action)
			} else {
				if err := sendEscalationSMS(cfg, beadID, severity, description); err != nil {
					status.Error = err.Error()
					style.PrintWarning("sms send failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  📱 SMS sent to %s\n", cfg.Contacts.HumanSMS)
				}
			}
			statuses = append(statuses, status)

		case action == "slack":
			status := deliveryStatus{Channel: "slack", Target: "slack", Severity: severity}
			if cfg.Contacts.SlackScript != "" {
				msg := formatEscalationSlackText(beadID, severity, description)
				cmd := exec.Command(cfg.Contacts.SlackScript, msg)
				if out, err := cmd.CombinedOutput(); err != nil {
					status.Error = fmt.Sprintf("script failed: %v: %s", err, string(out))
					style.PrintWarning("slack script failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  💬 Posted to Slack via script\n")
				}
			} else if cfg.Contacts.SlackWebhook != "" {
				if err := sendEscalationSlack(cfg, beadID, severity, description); err != nil {
					status.Error = err.Error()
					style.PrintWarning("slack post failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  💬 Posted to Slack\n")
				}
			} else {
				status.Warning = "contacts.slack_script or contacts.slack_webhook not configured"
				style.PrintWarning("slack action skipped: no slack_script or slack_webhook configured in settings/escalation.json")
			}
			statuses = append(statuses, status)

		case action == "log":
			status := deliveryStatus{Channel: "log", Target: "log", Severity: severity}
			if err := writeEscalationLog(townRoot, beadID, severity, description); err != nil {
				status.Error = err.Error()
				style.PrintWarning("log write failed: %v", err)
			} else {
				status.RuntimeNotified = true
				fmt.Printf("  📝 Logged to escalation log\n")
			}
			statuses = append(statuses, status)
		}
	}
	return statuses
}

// formatEscalationSlackText builds the Slack message text for an escalation.
func formatEscalationSlackText(beadID, severity, description string) string {
	severityEmoji := map[string]string{"critical": "🔴", "high": "🟠", "medium": "🟡"}
	emoji := severityEmoji[severity]
	if emoji == "" {
		emoji = "⚪"
	}
	return fmt.Sprintf("%s *[%s] Escalation %s*\n%s\n_Acknowledge: `gt escalate ack %s`_",
		emoji, strings.ToUpper(severity), beadID, description, beadID)
}

// sendEscalationEmail sends an escalation notification via SMTP.
func sendEscalationEmail(cfg *config.EscalationConfig, beadID, severity, description string) error {
	host := cfg.Contacts.SMTPHost
	port := cfg.Contacts.SMTPPort
	if port == "" {
		port = "587"
	}
	from := cfg.Contacts.SMTPFrom
	if from == "" {
		from = "gastown@localhost"
	}
	to := cfg.Contacts.HumanEmail
	subject := fmt.Sprintf("[Gas Town %s] %s", strings.ToUpper(severity), description)

	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+
		"Gas Town Escalation\r\n"+
		"====================\r\n"+
		"Bead: %s\r\n"+
		"Severity: %s\r\n"+
		"Description: %s\r\n\r\n"+
		"Acknowledge: gt escalate ack %s\r\n",
		from, to, subject, beadID, strings.ToUpper(severity), description, beadID)

	addr := fmt.Sprintf("%s:%s", host, port)

	var auth smtp.Auth
	if cfg.Contacts.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.Contacts.SMTPUser, cfg.Contacts.SMTPPass, host)
	}

	return smtp.SendMail(addr, auth, from, []string{to}, []byte(body))
}

// sendEscalationSlack posts an escalation notification to a Slack webhook.
func sendEscalationSlack(cfg *config.EscalationConfig, beadID, severity, description string) error {
	severityEmoji := map[string]string{
		"critical": "🔴",
		"high":     "🟠",
		"medium":   "🟡",
	}
	emoji := severityEmoji[severity]
	if emoji == "" {
		emoji = "⚪"
	}

	payload := map[string]string{
		"text": fmt.Sprintf("%s *[%s] Escalation %s*\n%s\n_Acknowledge: `gt escalate ack %s`_",
			emoji, strings.ToUpper(severity), beadID, description, beadID),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling slack payload: %w", err)
	}

	resp, err := http.Post(cfg.Contacts.SlackWebhook, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("posting to slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// sendEscalationSMS posts an escalation notification via SMS webhook (e.g. Twilio).
func sendEscalationSMS(cfg *config.EscalationConfig, beadID, severity, description string) error {
	payload := map[string]string{
		"to":   cfg.Contacts.HumanSMS,
		"body": fmt.Sprintf("[Gas Town %s] %s (bead: %s)", strings.ToUpper(severity), description, beadID),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling sms payload: %w", err)
	}

	resp, err := http.Post(cfg.Contacts.SMSWebhook, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("posting to sms webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sms webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// writeEscalationLog appends an escalation entry to the log file.
func writeEscalationLog(townRoot, beadID, severity, description string) error {
	logDir := fmt.Sprintf("%s/logs", townRoot)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}
	logPath := fmt.Sprintf("%s/escalations.log", logDir)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	entry := fmt.Sprintf("%s [%s] %s: %s\n", time.Now().Format(time.RFC3339), strings.ToUpper(severity), beadID, description)
	_, err = f.WriteString(entry)
	return err
}

// formatEscalationMailBody builds the body of the initial escalation notification mail.
func formatEscalationMailBody(beadID, severity, reason, from, related string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Escalation ID: %s", beadID))
	lines = append(lines, fmt.Sprintf("Severity: %s", severity))
	lines = append(lines, fmt.Sprintf("From: %s", from))
	if reason != "" {
		lines = append(lines, "")
		lines = append(lines, "Reason:")
		lines = append(lines, reason)
	}
	if related != "" {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Related: %s", related))
	}
	lines = append(lines, "")
	lines = append(lines, "---")
	lines = append(lines, "To acknowledge: gt escalate ack "+beadID)
	lines = append(lines, "To close: gt escalate close "+beadID+" --reason \"resolution\"")
	return strings.Join(lines, "\n")
}

// detectSenderFallback is a backup identity resolver used when detectSender is
// unavailable. The primary detectSender lives in mail_identity.go.
func detectSenderFallback() string {
	// Try BD_ACTOR first (most common in agent context)
	if actor := os.Getenv("BD_ACTOR"); actor != "" {
		return actor
	}
	// Try GT_ROLE
	if role := os.Getenv("GT_ROLE"); role != "" {
		return role
	}
	return ""
}
