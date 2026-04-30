package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// This file implements the `gt escalate` command entry point. The escalate
// subcommands and helpers are split across sibling files in this package:
//
//   escalate_list.go      — list/show + display helpers (severityEmoji, formatRelativeTime)
//   escalate_ack_close.go — ack and close handlers
//   escalate_stale.go     — stale reescalation (runEscalateStale, getNextSeverity, reescalation mail body)
//   escalate_notify.go    — external delivery: email, slack, sms, log, and shared types/helpers
//                           (deliveryStatus, extractMailTargetsFromActions, executeExternalActions,
//                            formatEscalationMailBody, detectSenderFallback)
//
// All of these files live in the same `cmd` package, so the split is purely
// organizational — symbols remain mutually accessible.

// runEscalate creates a new escalation bead and routes it to the configured
// targets for the given severity (mail to agents plus any external channels
// such as email/slack/sms/log). It is wired as the RunE for the `gt escalate`
// root command in escalate.go.
func runEscalate(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read reason from stdin (avoids shell quoting issues)
	if escalateStdin {
		if escalateReason != "" {
			return fmt.Errorf("cannot use --stdin with --reason/-r")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		escalateReason = strings.TrimRight(string(data), "\n")
	}

	// Require at least a description when creating an escalation
	if len(args) == 0 {
		return cmd.Help()
	}

	description := strings.Join(args, " ")

	// Validate severity
	severity := strings.ToLower(escalateSeverity)
	if !config.IsValidSeverity(severity) {
		return fmt.Errorf("invalid severity '%s': must be critical, high, medium, or low", escalateSeverity)
	}

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load escalation config
	escalationConfig, err := config.LoadOrCreateEscalationConfig(config.EscalationConfigPath(townRoot))
	if err != nil {
		return fmt.Errorf("loading escalation config: %w", err)
	}

	// Detect agent identity
	agentID := detectSender()
	if agentID == "" {
		agentID = "unknown"
	}

	// Dry run mode
	if escalateDryRun {
		actions := escalationConfig.GetRouteForSeverity(severity)
		targets := extractMailTargetsFromActions(actions)
		fmt.Printf("Would create escalation:\n")
		fmt.Printf("  Severity: %s\n", severity)
		fmt.Printf("  Description: %s\n", description)
		if escalateReason != "" {
			fmt.Printf("  Reason: %s\n", escalateReason)
		}
		if escalateSource != "" {
			fmt.Printf("  Source: %s\n", escalateSource)
		}
		fmt.Printf("  Actions: %s\n", strings.Join(actions, ", "))
		fmt.Printf("  Mail targets: %s\n", strings.Join(targets, ", "))
		return nil
	}

	// Create escalation bead
	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	fields := &beads.EscalationFields{
		Severity:    severity,
		Reason:      escalateReason,
		Source:      escalateSource,
		EscalatedBy: agentID,
		EscalatedAt: time.Now().Format(time.RFC3339),
		RelatedBead: escalateRelatedBead,
	}

	issue, err := bd.CreateEscalationBead(description, fields)
	if err != nil {
		return fmt.Errorf("creating escalation bead: %w", err)
	}

	// Get routing actions for this severity
	actions := escalationConfig.GetRouteForSeverity(severity)
	targets := extractMailTargetsFromActions(actions)

	// Send mail to each target (actions with "mail:" prefix)
	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	statuses := []deliveryStatus{{Channel: "bead", Created: true, Severity: severity}}
	for _, target := range targets {
		status := deliveryStatus{Target: target, Channel: "mail", Severity: severity, NotificationRoute: "mail+nudge"}
		msg := &mail.Message{
			From:     agentID,
			To:       target,
			Subject:  fmt.Sprintf("[%s] %s", strings.ToUpper(severity), description),
			Body:     formatEscalationMailBody(issue.ID, severity, escalateReason, agentID, escalateRelatedBead),
			Type:     mail.TypeEscalation,
			ThreadID: issue.ID,
		}

		// Set priority based on severity
		switch severity {
		case config.SeverityCritical:
			msg.Priority = mail.PriorityUrgent
		case config.SeverityHigh:
			msg.Priority = mail.PriorityHigh
		case config.SeverityMedium:
			msg.Priority = mail.PriorityNormal
		default:
			msg.Priority = mail.PriorityLow
		}

		if err := router.Send(msg); err != nil {
			status.Error = err.Error()
			statuses = append(statuses, status)
			style.PrintWarning("failed to send to %s: %v", target, err)
			continue
		}
		status.Persisted = true
		status.RuntimeNotified = true

		mailBeads := beads.New(beads.ResolveBeadsDir(townRoot))
		mailIssue, err := mailBeads.FindLatestIssueByTitleAndAssignee(msg.Subject, mail.AddressToIdentity(target))
		if err != nil {
			status.Warning = fmt.Sprintf("annotation lookup failed: %v", err)
			statuses = append(statuses, status)
			style.PrintWarning("failed to annotate escalation mail for %s: %v", target, err)
			continue
		}

		addLabels := []string{
			fmt.Sprintf("severity:%s", severity),
			fmt.Sprintf("escalation:%s", issue.ID),
		}
		if err := mailBeads.Update(mailIssue.ID, beads.UpdateOptions{AddLabels: addLabels}); err != nil {
			status.Warning = fmt.Sprintf("annotation update failed: %v", err)
			style.PrintWarning("failed to annotate escalation mail labels for %s: %v", target, err)
		} else {
			status.Annotated = true
		}
		statuses = append(statuses, status)
	}

	// Process external notification actions (email:, sms:, slack, log)
	statuses = append(statuses, executeExternalActions(actions, escalationConfig, issue.ID, severity, description, townRoot)...)

	// Log to activity feed
	payload := events.EscalationPayload(issue.ID, agentID, strings.Join(targets, ","), description)
	payload["severity"] = severity
	payload["actions"] = strings.Join(actions, ",")
	if escalateSource != "" {
		payload["source"] = escalateSource
	}
	_ = events.LogFeed(events.TypeEscalationSent, agentID, payload)

	// Output
	if escalateJSON {
		hasFailure := false
		for _, status := range statuses {
			if status.Error != "" {
				hasFailure = true
				break
			}
		}
		result := map[string]interface{}{
			"id":       issue.ID,
			"severity": severity,
			"actions":  actions,
			"targets":  targets,
			"delivery": statuses,
			"status":   map[bool]string{true: "partial_failure", false: "ok"}[hasFailure],
		}
		if escalateSource != "" {
			result["source"] = escalateSource
		}
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
	} else {
		emoji := severityEmoji(severity)
		fmt.Printf("%s Escalation created: %s\n", emoji, issue.ID)
		fmt.Printf("  Severity: %s\n", severity)
		if escalateSource != "" {
			fmt.Printf("  Source: %s\n", escalateSource)
		}
		fmt.Printf("  Routed to: %s\n", strings.Join(targets, ", "))
		for _, status := range statuses {
			if status.Error != "" {
				fmt.Printf("  Delivery issue [%s:%s]: %s\n", status.Channel, status.Target, status.Error)
			}
		}
	}

	return nil
}
