package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// TestFormatEscalationSlackText verifies the Slack message formatter
// renders severity emojis, uppercased severity, bead ID, description, and
// the ack helper command for all supported and fallback severities.
func TestFormatEscalationSlackText(t *testing.T) {
	tests := []struct {
		name        string
		beadID      string
		severity    string
		description string
		wantEmoji   string
		wantSev     string
	}{
		{
			name:        "critical severity uses red emoji",
			beadID:      "hq-crit1",
			severity:    "critical",
			description: "Site down",
			wantEmoji:   "🔴",
			wantSev:     "CRITICAL",
		},
		{
			name:        "high severity uses orange emoji",
			beadID:      "hq-high2",
			severity:    "high",
			description: "Build failing",
			wantEmoji:   "🟠",
			wantSev:     "HIGH",
		},
		{
			name:        "medium severity uses yellow emoji",
			beadID:      "hq-med3",
			severity:    "medium",
			description: "Slow queries",
			wantEmoji:   "🟡",
			wantSev:     "MEDIUM",
		},
		{
			name:        "unknown severity falls back to white circle",
			beadID:      "hq-unk4",
			severity:    "nonsense",
			description: "Weird thing",
			wantEmoji:   "⚪",
			wantSev:     "NONSENSE",
		},
		{
			name:        "empty severity falls back to white circle",
			beadID:      "hq-empty5",
			severity:    "",
			description: "No severity",
			wantEmoji:   "⚪",
			wantSev:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatEscalationSlackText(tt.beadID, tt.severity, tt.description)
			if !strings.Contains(got, tt.wantEmoji) {
				t.Errorf("expected emoji %q in output, got: %s", tt.wantEmoji, got)
			}
			if tt.wantSev != "" && !strings.Contains(got, tt.wantSev) {
				t.Errorf("expected uppercased severity %q in output, got: %s", tt.wantSev, got)
			}
			if !strings.Contains(got, tt.beadID) {
				t.Errorf("expected bead ID %q in output, got: %s", tt.beadID, got)
			}
			if !strings.Contains(got, tt.description) {
				t.Errorf("expected description %q in output, got: %s", tt.description, got)
			}
			// Every message must surface the acknowledgement command.
			wantAck := "gt escalate ack " + tt.beadID
			if !strings.Contains(got, wantAck) {
				t.Errorf("expected ack hint %q in output, got: %s", wantAck, got)
			}
		})
	}
}

// TestSendEscalationSlackSuccess spins up a test HTTP server that accepts
// the webhook payload and returns 200. We verify the payload is well-formed
// JSON containing the expected fields, and that sendEscalationSlack returns nil.
func TestSendEscalationSlackSuccess(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		gotBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.EscalationConfig{
		Contacts: config.EscalationContacts{
			SlackWebhook: srv.URL,
		},
	}

	err := sendEscalationSlack(cfg, "hq-abc", "critical", "Database unreachable")
	if err != nil {
		t.Fatalf("sendEscalationSlack returned error: %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", gotContentType)
	}

	var payload map[string]string
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body: %s", err, gotBody)
	}
	text, ok := payload["text"]
	if !ok {
		t.Fatalf("payload missing 'text' field: %s", gotBody)
	}
	for _, want := range []string{"CRITICAL", "hq-abc", "Database unreachable", "gt escalate ack hq-abc"} {
		if !strings.Contains(text, want) {
			t.Errorf("payload text missing %q: %s", want, text)
		}
	}
}

// TestSendEscalationSlackErrorStatus verifies that a non-2xx webhook response
// is surfaced as an error that includes the status code and response body.
func TestSendEscalationSlackErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid_payload"))
	}))
	defer srv.Close()

	cfg := &config.EscalationConfig{
		Contacts: config.EscalationContacts{SlackWebhook: srv.URL},
	}

	err := sendEscalationSlack(cfg, "hq-bad", "high", "Oops")
	if err == nil {
		t.Fatal("expected error from non-2xx status, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to include status 400, got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_payload") {
		t.Errorf("expected error to include response body, got: %v", err)
	}
}

// TestSendEscalationSlackBadURL verifies we surface a connection error when
// the webhook host is unreachable.
func TestSendEscalationSlackBadURL(t *testing.T) {
	cfg := &config.EscalationConfig{
		Contacts: config.EscalationContacts{
			// Use an invalid scheme to force http.Post to error synchronously.
			SlackWebhook: "http://127.0.0.1:1/slack-unreachable",
		},
	}
	err := sendEscalationSlack(cfg, "hq-x", "medium", "desc")
	if err == nil {
		t.Fatal("expected error when slack webhook is unreachable")
	}
	if !strings.Contains(err.Error(), "posting to slack") {
		t.Errorf("expected error prefix 'posting to slack', got: %v", err)
	}
}

// TestSendEscalationSMSSuccess verifies the SMS webhook integration:
// correct HTTP method (POST), JSON Content-Type, and payload fields.
func TestSendEscalationSMSSuccess(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		gotBody = body
		w.WriteHeader(http.StatusAccepted) // 202 still in 2xx range
	}))
	defer srv.Close()

	cfg := &config.EscalationConfig{
		Contacts: config.EscalationContacts{
			HumanSMS:   "+15551234567",
			SMSWebhook: srv.URL,
		},
	}

	err := sendEscalationSMS(cfg, "hq-sms1", "high", "Pager event")
	if err != nil {
		t.Fatalf("sendEscalationSMS returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}

	var payload map[string]string
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("body not valid JSON: %v; body: %s", err, gotBody)
	}
	if payload["to"] != "+15551234567" {
		t.Errorf("expected to=+15551234567, got %q", payload["to"])
	}
	for _, want := range []string{"HIGH", "hq-sms1", "Pager event"} {
		if !strings.Contains(payload["body"], want) {
			t.Errorf("sms body missing %q: %s", want, payload["body"])
		}
	}
}

// TestSendEscalationSMSErrorStatus verifies that a failing SMS webhook
// returns an error containing the status code.
func TestSendEscalationSMSErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("carrier_down"))
	}))
	defer srv.Close()

	cfg := &config.EscalationConfig{
		Contacts: config.EscalationContacts{
			HumanSMS:   "+15550000000",
			SMSWebhook: srv.URL,
		},
	}

	err := sendEscalationSMS(cfg, "hq-fail", "critical", "fire")
	if err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to include status 500, got: %v", err)
	}
	if !strings.Contains(err.Error(), "carrier_down") {
		t.Errorf("expected error to include response body, got: %v", err)
	}
}

// TestSendEscalationSMSBadURL verifies a transport-level error surfaces with
// the expected "posting to sms webhook" prefix.
func TestSendEscalationSMSBadURL(t *testing.T) {
	cfg := &config.EscalationConfig{
		Contacts: config.EscalationContacts{
			HumanSMS:   "+15550000000",
			SMSWebhook: "http://127.0.0.1:1/sms-unreachable",
		},
	}
	err := sendEscalationSMS(cfg, "hq-x", "low", "desc")
	if err == nil {
		t.Fatal("expected error when sms webhook is unreachable")
	}
	if !strings.Contains(err.Error(), "posting to sms webhook") {
		t.Errorf("expected error prefix 'posting to sms webhook', got: %v", err)
	}
}

// TestWriteEscalationLogAppends verifies that multiple calls to
// writeEscalationLog accumulate entries rather than overwriting.
func TestWriteEscalationLogAppends(t *testing.T) {
	tmpDir := t.TempDir()
	if err := writeEscalationLog(tmpDir, "hq-1", "high", "first"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeEscalationLog(tmpDir, "hq-2", "critical", "second"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	logPath := filepath.Join(tmpDir, "logs", "escalations.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	text := string(data)
	for _, want := range []string{"hq-1", "first", "hq-2", "second", "[HIGH]", "[CRITICAL]"} {
		if !strings.Contains(text, want) {
			t.Errorf("log missing %q, got: %s", want, text)
		}
	}
	// Two entries ⇒ two newlines.
	if strings.Count(text, "\n") < 2 {
		t.Errorf("expected at least 2 log lines, got: %s", text)
	}
}

// TestWriteEscalationLogCreatesDirectory verifies the logs/ directory is
// created on first call even when it doesn't exist.
func TestWriteEscalationLogCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	logsDir := filepath.Join(tmpDir, "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Fatalf("expected logs/ to not exist yet")
	}
	if err := writeEscalationLog(tmpDir, "hq-mk", "medium", "test"); err != nil {
		t.Fatalf("writeEscalationLog: %v", err)
	}
	info, err := os.Stat(logsDir)
	if err != nil {
		t.Fatalf("logs/ not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("logs/ exists but is not a directory")
	}
}

// TestRunEscalateDryRunNoMutations verifies that --dry-run does not create
// any beads or touch the filesystem beyond printing. We run in a temp workspace
// with no Gas Town marker so we can assert the dry-run path short-circuits
// before network or filesystem mutation.
//
// Note: the dry-run path in runEscalate needs a workspace root, so we set up
// a minimal workspace marker. This test focuses on: no error + prints expected
// lines, without calling beads / mail.
func TestRunEscalateDryRunNoMutations(t *testing.T) {
	// Save and restore package-level flags.
	origSeverity, origReason, origStdin, origDryRun, origSource, origRelated := escalateSeverity, escalateReason, escalateStdin, escalateDryRun, escalateSource, escalateRelatedBead
	defer func() {
		escalateSeverity = origSeverity
		escalateReason = origReason
		escalateStdin = origStdin
		escalateDryRun = origDryRun
		escalateSource = origSource
		escalateRelatedBead = origRelated
	}()

	// Move to a temp dir with a real workspace marker (mayor/town.json) so
	// workspace.FindFromCwdOrError succeeds via CWD detection, not via the
	// GT_TOWN_ROOT/GT_ROOT env var fallback. Unsetting the env vars ensures
	// we exercise the CWD-based path even when the developer's shell has
	// them set (which masked this test's bogus .gastown marker — it passed
	// locally via env fallback and only surfaced in CI).
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"type":"town","name":"test","version":1}`), 0o644); err != nil {
		t.Fatalf("write mayor/town.json: %v", err)
	}
	origWD, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	escalateSeverity = "medium"
	escalateReason = ""
	escalateStdin = false
	escalateDryRun = true
	escalateSource = "test-source"
	escalateRelatedBead = ""

	// Capture stdout while the dry-run executes.
	stdout := captureStdout(t, func() {
		if err := runEscalate(escalateCmd, []string{"dry", "run", "description"}); err != nil {
			t.Fatalf("runEscalate dry-run returned error: %v", err)
		}
	})

	wantIn := []string{
		"Would create escalation",
		"Severity: medium",
		"Description: dry run description",
		"Source: test-source",
	}
	for _, want := range wantIn {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run output missing %q, got:\n%s", want, stdout)
		}
	}

	// Ensure no escalations.log was written (dry-run should not touch logs).
	if _, err := os.Stat(filepath.Join(tmpDir, "logs", "escalations.log")); !os.IsNotExist(err) {
		t.Errorf("dry-run should not create escalations.log; stat err: %v", err)
	}
}

// TestRunEscalateInvalidSeverityNoArgsReturnsHelp verifies the precedence:
// empty args return help (nil error) BEFORE severity validation kicks in.
func TestRunEscalateEmptyArgsBeforeSeverityCheck(t *testing.T) {
	origSeverity, origReason, origStdin := escalateSeverity, escalateReason, escalateStdin
	defer func() {
		escalateSeverity = origSeverity
		escalateReason = origReason
		escalateStdin = origStdin
	}()

	// Even an invalid severity should be ignored when there are no args —
	// help output wins.
	escalateSeverity = "emergency"
	escalateReason = ""
	escalateStdin = false

	err := runEscalate(escalateCmd, []string{})
	if err != nil {
		t.Errorf("expected nil error (help) with empty args, got: %v", err)
	}
}

// TestRunEscalateStdinBlocksReason verifies the --stdin / --reason mutual
// exclusion check happens before workspace lookup. This is important for
// UX: the user gets an immediate, clear error even outside a workspace.
func TestRunEscalateStdinBlocksReasonEarly(t *testing.T) {
	origSeverity, origReason, origStdin := escalateSeverity, escalateReason, escalateStdin
	defer func() {
		escalateSeverity = origSeverity
		escalateReason = origReason
		escalateStdin = origStdin
	}()

	escalateStdin = true
	escalateReason = "should conflict"
	escalateSeverity = "medium"

	// Even outside a workspace, the flag conflict should be returned.
	tmpDir := t.TempDir()
	origWD, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	err := runEscalate(escalateCmd, []string{"test"})
	if err == nil {
		t.Fatal("expected error for --stdin + --reason, got nil")
	}
	if !strings.Contains(err.Error(), "cannot use --stdin with --reason/-r") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestDeliveryStatusOmitsEmptyFields confirms json tags with omitempty
// produce a compact payload when fields are zero-valued. This guards against
// regressions in the JSON contract consumed by CLI --json output.
func TestDeliveryStatusOmitsEmptyFields(t *testing.T) {
	status := deliveryStatus{Channel: "bead", Created: true}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(data)
	// Zero-valued fields must be omitted.
	unwanted := []string{"target", "persisted", "runtime_notified", "annotated", "severity", "error", "warning", "notification_route"}
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Errorf("expected field %q to be omitted, got: %s", u, text)
		}
	}
	// Present fields must round-trip.
	if !strings.Contains(text, `"channel":"bead"`) {
		t.Errorf("channel missing: %s", text)
	}
	if !strings.Contains(text, `"created":true`) {
		t.Errorf("created missing: %s", text)
	}
}

// --- helpers ---

// (captureStdout is defined in prime_test.go and reused here.)

