package daemon

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

const (
	defaultFailureClassifierInterval = 15 * time.Minute
	failureClassifierPluginLabel     = "plugin:failure-classifier"
)

// FailureClassifierConfig holds configuration for the failure_classifier patrol.
// This patrol watches for main_branch_test escalations, pattern-matches the
// failure body against known code-issue signatures, and auto-files rig beads
// for actionable failures — reducing the operator triage burden.
type FailureClassifierConfig struct {
	// Enabled controls whether the failure classifier runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "15m").
	IntervalStr string `json:"interval,omitempty"`

	// SignaturesFile is the path to the JSON signatures config file.
	// If relative, it is resolved relative to the town root.
	// Defaults to mayor/failure-signatures.json.
	// If the file does not exist, built-in signatures are used.
	SignaturesFile string `json:"signatures_file,omitempty"`
}

// failureClassifierInterval returns the configured interval, or the default (15m).
func failureClassifierInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.FailureClassifier != nil {
		if config.Patrols.FailureClassifier.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.FailureClassifier.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultFailureClassifierInterval
}

// FailureSignature describes a known failure pattern and how to respond to it.
// Operators can add signatures to mayor/failure-signatures.json without code changes.
type FailureSignature struct {
	// ID is a unique slug for this signature (used in fingerprinting).
	ID string `json:"id"`

	// Description is a human-readable explanation of what this signature matches.
	Description string `json:"description"`

	// Patterns is a list of Go regular expressions matched against the full
	// escalation description. If ANY pattern matches, the signature fires.
	Patterns []string `json:"patterns"`

	// BeadTitle is the title for the auto-filed bead.
	// Supports {rig}, {gate}, {escalation_id} substitutions.
	BeadTitle string `json:"bead_title"`

	// BeadBody is the description for the auto-filed bead.
	// Supports {rig}, {gate}, {snippet}, {escalation_id} substitutions.
	BeadBody string `json:"bead_body"`

	// Priority is the bead priority ("P1"–"P4"). Defaults to "P3".
	Priority string `json:"priority"`

	// Labels are additional labels to add to the auto-filed bead.
	Labels []string `json:"labels"`
}

// builtinSignatures covers the failure modes observed in the session that
// motivated this patrol (see gs-0wz bead description).
var builtinSignatures = []FailureSignature{
	{
		ID:          "ts-import-error",
		Description: "TypeScript named/default import error",
		Patterns: []string{
			`Type error: Module '.*' has no exported member`,
			`error TS2305: Module '.*' has no exported member`,
			`error TS2614: Module '.*' has no exported member`,
			`error TS2724: .* has no exported member`,
		},
		BeadTitle: "TypeScript import error: {rig}",
		BeadBody: `TypeScript named/default import error detected on main branch of {rig}.

Gate: {gate}

Auto-filed by failure_classifier from escalation {escalation_id}.

Failure snippet:
` + "```" + `
{snippet}
` + "```",
		Priority: "P3",
		Labels:   []string{"type:build", "lang:typescript"},
	},
	{
		ID:          "pre-commit-changes",
		Description: "Uncommitted formatter output (pre-commit hook made changes)",
		Patterns: []string{
			`pre-commit hook\(s\) made changes`,
			`files were modified by this hook`,
		},
		BeadTitle: "Uncommitted formatter output: {rig}",
		BeadBody: `Pre-commit hooks modified files on main branch of {rig} that were not committed.
Run the formatter locally, commit the result, and push.

Gate: {gate}

Auto-filed by failure_classifier from escalation {escalation_id}.

Failure snippet:
` + "```" + `
{snippet}
` + "```",
		Priority: "P3",
		Labels:   []string{"type:formatter"},
	},
	{
		ID:          "python-attribute-error",
		Description: "Python AttributeError: renamed or removed symbol",
		Patterns: []string{
			`AttributeError: '.*' object has no attribute`,
			`AttributeError: module '.*' has no attribute`,
			`AttributeError: type object '.*' has no attribute`,
		},
		BeadTitle: "Python AttributeError on main: {rig}",
		BeadBody: `Python AttributeError detected on main branch of {rig}.
A symbol was likely renamed or removed without updating all callers.

Gate: {gate}

Auto-filed by failure_classifier from escalation {escalation_id}.

Failure snippet:
` + "```" + `
{snippet}
` + "```",
		Priority: "P3",
		Labels:   []string{"type:runtime", "lang:python"},
	},
	{
		ID:          "golden-snapshot-drift",
		Description: "Rendered HTML does not match golden snapshot",
		Patterns: []string{
			`Rendered HTML does not match golden`,
			`Failed: Rendered HTML does not match golden`,
			`snapshot.*mismatch`,
			`does not match the stored snapshot`,
		},
		BeadTitle: "Golden snapshot drift: {rig}",
		BeadBody: `Golden snapshot drift detected on main branch of {rig}.
Update the golden files with the new rendered output.

Gate: {gate}

Auto-filed by failure_classifier from escalation {escalation_id}.

Failure snippet:
` + "```" + `
{snippet}
` + "```",
		Priority: "P3",
		Labels:   []string{"type:snapshot"},
	},
	{
		ID:          "mypy-error",
		Description: "mypy static type checking failure",
		Patterns: []string{
			`Found \d+ error(s)? in \d+ file`,
			`mypy.*: error:`,
		},
		BeadTitle: "mypy type error on main: {rig}",
		BeadBody: `mypy type checking failed on main branch of {rig}.

Gate: {gate}

Auto-filed by failure_classifier from escalation {escalation_id}.

Failure snippet:
` + "```" + `
{snippet}
` + "```",
		Priority: "P3",
		Labels:   []string{"type:lint", "lang:python"},
	},
	{
		ID:          "nbstripout-dirty",
		Description: "Jupyter notebook has unstripped output",
		Patterns: []string{
			`nbstripout`,
			`Notebook output not stripped`,
		},
		BeadTitle: "Notebook output unstripped: {rig}",
		BeadBody: `Jupyter notebook with unstripped output detected on main branch of {rig}.
Run nbstripout on the affected notebooks and commit.

Gate: {gate}

Auto-filed by failure_classifier from escalation {escalation_id}.

Failure snippet:
` + "```" + `
{snippet}
` + "```",
		Priority: "P3",
		Labels:   []string{"type:formatter", "lang:python"},
	},
}

// compiledSignature is a FailureSignature with compiled regex patterns.
type compiledSignature struct {
	sig      FailureSignature
	patterns []*regexp.Regexp
}

func (c *compiledSignature) matchesAny(text string) bool {
	for _, p := range c.patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// compileSignatures compiles regex patterns in each signature.
// Returns an error if any pattern is invalid.
func compileSignatures(sigs []FailureSignature) ([]*compiledSignature, error) {
	result := make([]*compiledSignature, 0, len(sigs))
	for _, sig := range sigs {
		if sig.ID == "" {
			continue
		}
		comp := &compiledSignature{sig: sig}
		for _, pat := range sig.Patterns {
			r, err := regexp.Compile(pat)
			if err != nil {
				return nil, fmt.Errorf("signature %s: invalid pattern %q: %w", sig.ID, pat, err)
			}
			comp.patterns = append(comp.patterns, r)
		}
		result = append(result, comp)
	}
	return result, nil
}

// loadFailureSignatures loads signatures from the configured file (if present),
// falling back to built-in defaults when the file is absent or unreadable.
func (d *Daemon) loadFailureSignatures() []FailureSignature {
	sigFile := ""
	if d.patrolConfig != nil && d.patrolConfig.Patrols != nil && d.patrolConfig.Patrols.FailureClassifier != nil {
		sigFile = d.patrolConfig.Patrols.FailureClassifier.SignaturesFile
	}
	if sigFile == "" {
		sigFile = filepath.Join(d.config.TownRoot, "mayor", "failure-signatures.json")
	} else if !filepath.IsAbs(sigFile) {
		sigFile = filepath.Join(d.config.TownRoot, sigFile)
	}

	data, err := os.ReadFile(sigFile) //nolint:gosec // G304: path from trusted config
	if err != nil {
		return builtinSignatures
	}

	var sigs []FailureSignature
	if err := json.Unmarshal(data, &sigs); err != nil {
		d.logger.Printf("failure_classifier: invalid signatures file %s: %v — using built-ins", sigFile, err)
		return builtinSignatures
	}
	d.logger.Printf("failure_classifier: loaded %d signatures from %s", len(sigs), sigFile)
	return sigs
}

// classifierFingerprint computes a stable 12-char hex dedupe fingerprint.
// Fingerprint dimensions: rig × signature_id.
// Invariant under repeated escalations, rig-level drift, and patrol restarts.
func classifierFingerprint(rigName, signatureID string) string {
	h := sha1.New() //nolint:gosec // G401: sha1 used for stable fingerprint, not cryptography
	fmt.Fprintf(h, "%s::%s", rigName, signatureID)
	return fmt.Sprintf("%x", h.Sum(nil))[:12]
}

// mainBranchEscalation holds parsed data from a main_branch_test escalation.
type mainBranchEscalation struct {
	ID          string
	Title       string
	Description string // full description from the bead JSON
	Rigs        []string
}

// parseMainBranchEscalation extracts rig names from an escalation bead's description.
// Rig names are identified by searching for "<rigname>: " in the description text,
// cross-referenced with the list of known rigs.
func parseMainBranchEscalation(description string, knownRigs []string) []string {
	var found []string
	seen := make(map[string]bool)
	for _, rig := range knownRigs {
		if !seen[rig] && strings.Contains(description, rig+": ") {
			found = append(found, rig)
			seen[rig] = true
		}
	}
	return found
}

// runFailureClassifier is the main patrol function.
// It queries open, unacked main_branch_test escalations, pattern-matches them
// against known failure signatures, auto-files rig beads for matches (with
// fingerprint-based dedup), and acks all processed escalations.
func (d *Daemon) runFailureClassifier() {
	if !d.isPatrolActive("failure_classifier") {
		return
	}

	d.logger.Printf("failure_classifier: starting patrol cycle")

	sigs := d.loadFailureSignatures()
	compiled, err := compileSignatures(sigs)
	if err != nil {
		d.logger.Printf("failure_classifier: signature compilation error: %v", err)
		return
	}
	if len(compiled) == 0 {
		d.logger.Printf("failure_classifier: no signatures loaded, skipping cycle")
		return
	}

	escalations, err := d.listUnackedMainBranchEscalations()
	if err != nil {
		d.logger.Printf("failure_classifier: failed to list escalations: %v", err)
		return
	}

	if len(escalations) == 0 {
		d.logger.Printf("failure_classifier: no unacked main_branch_test escalations")
		return
	}

	d.logger.Printf("failure_classifier: classifying %d escalation(s)", len(escalations))

	knownRigs := d.getKnownRigs()

	var totalMatched, totalMissed int

	for _, esc := range escalations {
		matched := d.classifyEscalation(esc, compiled, knownRigs)
		if matched {
			totalMatched++
		} else {
			totalMissed++
		}

		// Always ack — prevents escalation pile-up regardless of match result.
		d.ackClassifierEscalation(esc.ID, matched)
	}

	d.logger.Printf("failure_classifier: cycle complete — total=%d matched=%d missed=%d",
		len(escalations), totalMatched, totalMissed)
}

// classifyEscalation attempts to match an escalation against all signatures,
// filing beads for any that match. Returns true if at least one signature matched.
func (d *Daemon) classifyEscalation(esc mainBranchEscalation, compiled []*compiledSignature, knownRigs []string) bool {
	rigs := parseMainBranchEscalation(esc.Description, knownRigs)
	if len(rigs) == 0 {
		d.logger.Printf("failure_classifier: %s: no known rigs identified in failure body", esc.ID)
	}

	anyMatch := false
	for _, comp := range compiled {
		if !comp.matchesAny(esc.Description) {
			continue
		}
		anyMatch = true

		if len(rigs) == 0 {
			d.logger.Printf("failure_classifier: %s: sig=%s matched but no rig identified — cannot file bead",
				esc.ID, comp.sig.ID)
			continue
		}

		for _, rigName := range rigs {
			fp := classifierFingerprint(rigName, comp.sig.ID)
			fpLabel := "fingerprint:" + fp

			rigDir := beads.GetRigDirForName(d.config.TownRoot, rigName)
			if rigDir == "" {
				d.logger.Printf("failure_classifier: %s: sig=%s rig=%s: no beads dir found, skipping",
					esc.ID, comp.sig.ID, rigName)
				continue
			}

			if d.classifierBeadExists(rigDir, fpLabel) {
				d.logger.Printf("failure_classifier: %s: sig=%s rig=%s: deduped (open bead with fp=%s exists)",
					esc.ID, comp.sig.ID, rigName, fp)
				continue
			}

			if err := d.fileClassifierBead(rigName, rigDir, comp.sig, esc, fpLabel); err != nil {
				d.logger.Printf("failure_classifier: %s: sig=%s rig=%s: failed to file bead: %v",
					esc.ID, comp.sig.ID, rigName, err)
			} else {
				d.logger.Printf("failure_classifier: %s: sig=%s rig=%s: filed new bead (fp=%s)",
					esc.ID, comp.sig.ID, rigName, fp)
			}
		}
	}

	return anyMatch
}

// listUnackedMainBranchEscalations returns open escalation beads whose title
// starts with "main_branch_test:" and that have not yet been acked.
func (d *Daemon) listUnackedMainBranchEscalations() ([]mainBranchEscalation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"list",
		"--label=gt:escalation",
		"--status=open",
		"--json",
		"--limit=100",
	)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}

	var all []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	var result []mainBranchEscalation
	for _, issue := range all {
		if !strings.HasPrefix(issue.Title, "main_branch_test:") {
			continue
		}
		if sliceContains(issue.Labels, "acked") {
			continue
		}
		result = append(result, mainBranchEscalation{
			ID:          issue.ID,
			Title:       issue.Title,
			Description: issue.Description,
		})
	}
	return result, nil
}

// classifierBeadExists returns true if an open bead with the failure-classifier
// plugin label and the given fingerprint label exists in rigDir.
func (d *Daemon) classifierBeadExists(rigDir, fpLabel string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"list",
		"--label="+failureClassifierPluginLabel+","+fpLabel,
		"--status=open",
		"--json",
	)
	cmd.Dir = rigDir
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	var issues []json.RawMessage
	return json.Unmarshal(out, &issues) == nil && len(issues) > 0
}

// fileClassifierBead creates a new bead in the rig for the matched failure signature.
func (d *Daemon) fileClassifierBead(rigName, rigDir string, sig FailureSignature, esc mainBranchEscalation, fpLabel string) error {
	gate := extractFailureGate(esc.Description, rigName)
	snippet := extractFailureSnippet(esc.Description, rigName, 20)

	r := strings.NewReplacer(
		"{rig}", rigName,
		"{gate}", gate,
		"{snippet}", snippet,
		"{escalation_id}", esc.ID,
	)
	title := r.Replace(sig.BeadTitle)
	body := r.Replace(sig.BeadBody)

	priority := sig.Priority
	if priority == "" {
		priority = "P3"
	}

	allLabels := append([]string{failureClassifierPluginLabel, fpLabel}, sig.Labels...)
	labelStr := strings.Join(allLabels, ",")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"create",
		"--title="+title,
		"--description="+body,
		"--type=task",
		"--priority="+priority,
		"--label="+labelStr,
	)
	cmd.Dir = rigDir
	cmd.Env = append(os.Environ(), "BD_ACTOR=failure-classifier")
	setSysProcAttr(cmd)

	return cmd.Run()
}

// ackClassifierEscalation acks an escalation via gt escalate ack.
// matched indicates whether a signature matched (logged for observability).
func (d *Daemon) ackClassifierEscalation(id string, matched bool) {
	if id == "" {
		return
	}
	matchStr := "no-match"
	if matched {
		matchStr = "matched"
	}
	d.logger.Printf("failure_classifier: acking %s (%s)", id, matchStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.gtPath, "escalate", "ack", id) //nolint:gosec // G204: args constructed internally
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=failure-classifier")
	setSysProcAttr(cmd)

	if err := cmd.Run(); err != nil {
		d.logger.Printf("failure_classifier: failed to ack %s: %v", id, err)
	}
}

// extractFailureGate extracts the gate name from a failure description for the
// given rig. Expects lines of the form "<rig>: gate "<name>": ...".
func extractFailureGate(description, rigName string) string {
	prefix := rigName + `: gate "`
	for _, line := range strings.Split(description, "\n") {
		if idx := strings.Index(line, prefix); idx >= 0 {
			after := line[idx+len(prefix):]
			if end := strings.Index(after, `"`); end > 0 {
				return after[:end]
			}
		}
	}
	return "unknown"
}

// extractFailureSnippet extracts up to maxLines lines of failure context for
// the given rig from the escalation description. Falls back to the full
// description (truncated) if no rig-specific section is found.
func extractFailureSnippet(description, rigName string, maxLines int) string {
	lines := strings.Split(description, "\n")
	rigPrefix := rigName + ": "

	// Find the line that introduces this rig's failure, then capture
	// subsequent lines until the next rig's section or a structured field.
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, rigPrefix) {
			start = i
			break
		}
	}

	var snippet []string
	if start >= 0 {
		for i := start; i < len(lines) && len(snippet) < maxLines; i++ {
			line := lines[i]
			// Stop at the next rig section or structured escalation field.
			if i > start && strings.HasPrefix(line, rigPrefix) {
				break
			}
			if isEscalationStructuredField(line) {
				break
			}
			snippet = append(snippet, line)
		}
	}

	if len(snippet) == 0 {
		// Fall back: first maxLines lines of description.
		if len(lines) > maxLines {
			lines = lines[:maxLines]
		}
		snippet = lines
	}

	return strings.Join(snippet, "\n")
}

// escalationStructuredFields are the key: prefixes from FormatEscalationDescription.
// Matching these marks the end of the embedded failure body in a description.
var escalationStructuredFields = []string{
	"severity:", "reason:", "source:", "escalated_by:", "escalated_at:",
	"acked_by:", "acked_at:", "closed_by:", "closed_reason:", "related_bead:",
	"original_severity:", "reescalation_count:", "last_reescalated_at:", "last_reescalated_by:",
}

// isEscalationStructuredField returns true if line is a "key: value" field
// from FormatEscalationDescription. These mark the end of the failure body.
func isEscalationStructuredField(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, f := range escalationStructuredFields {
		if strings.HasPrefix(lower, f) {
			return true
		}
	}
	return false
}
