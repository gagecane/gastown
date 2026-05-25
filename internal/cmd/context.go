package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

// Default context window configuration.
const (
	defaultMaxContextTokens = 200000
)

var contextCmd = &cobra.Command{
	Use:     "context",
	GroupID: GroupDiag,
	Short:   "Show context window usage for the current session",
	Long: `Display the context window token usage for the current AI agent session.

Reads the Claude Code JSONL transcript to determine how much of the context
window has been consumed. Useful for agents to decide when to hand off work.

Examples:
  gt context             # Show usage summary
  gt context --usage     # Same as above (explicit flag)
  gt context --json      # Machine-readable JSON output`,
	RunE: runContext,
}

func init() {
	contextCmd.Flags().Bool("usage", false, "Show context usage (default behavior)")
	contextCmd.Flags().Bool("json", false, "Output in JSON format")
	rootCmd.AddCommand(contextCmd)

	// Add to beads-exempt commands (no Dolt needed for this diagnostic)
	beadsExemptCommands["context"] = true
}

// contextUsageResult holds the context usage data for JSON output.
type contextUsageResult struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CacheRead    int     `json:"cache_read_tokens"`
	CacheCreate  int     `json:"cache_creation_tokens"`
	TotalInput   int     `json:"total_input"`
	MaxTokens    int     `json:"max_tokens"`
	UsageRatio   float64 `json:"usage_ratio"`
	UsagePct     int     `json:"usage_pct"`
	RemainingK   int     `json:"remaining_k"`
	Level        string  `json:"level"` // "ok", "warn", "soft_gate", "hard_gate"
	Role         string  `json:"role,omitempty"`
	Transcript   string  `json:"transcript,omitempty"`
}

func runContext(cmd *cobra.Command, args []string) error {
	jsonFlag, _ := cmd.Flags().GetBool("json")

	maxTokens := defaultMaxContextTokens
	if envMax := os.Getenv("GT_CONTEXT_BUDGET_MAX_TOKENS"); envMax != "" {
		if n := parseContextInt(envMax); n > 0 {
			maxTokens = n
		}
	}

	// Resolve thresholds from env (same as context-budget-guard.sh)
	warnThreshold := 0.75
	softGateThreshold := 0.85
	hardGateThreshold := 0.92
	if v := os.Getenv("GT_CONTEXT_BUDGET_WARN"); v != "" {
		if f := parseContextFloat(v); f > 0 {
			warnThreshold = f
		}
	}
	if v := os.Getenv("GT_CONTEXT_BUDGET_SOFT_GATE"); v != "" {
		if f := parseContextFloat(v); f > 0 {
			softGateThreshold = f
		}
	}
	if v := os.Getenv("GT_CONTEXT_BUDGET_HARD_GATE"); v != "" {
		if f := parseContextFloat(v); f > 0 {
			hardGateThreshold = f
		}
	}

	// Allow pre-computed token count injection (same as the shell guard)
	var inputTokens, outputTokens, cacheRead, cacheCreate int
	var transcriptPath string

	if envTokens := os.Getenv("GT_CONTEXT_BUDGET_TOKENS"); envTokens != "" {
		inputTokens = parseContextInt(envTokens)
	} else {
		// Parse from Claude Code transcript
		usage, path, err := readLatestUsageFromTranscript()
		if err != nil {
			if jsonFlag {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"error": err.Error(),
				})
			}
			return fmt.Errorf("cannot read context usage: %w", err)
		}
		inputTokens = usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		outputTokens = usage.OutputTokens
		cacheRead = usage.CacheReadInputTokens
		cacheCreate = usage.CacheCreationInputTokens
		transcriptPath = path
	}

	if inputTokens <= 0 {
		if jsonFlag {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{
				"error": "no token usage data found in transcript",
			})
		}
		fmt.Println("No context usage data available (no assistant messages with usage found)")
		return nil
	}

	// Calculate
	ratio := float64(inputTokens) / float64(maxTokens)
	pct := int(ratio * 100)
	remainingK := (maxTokens - inputTokens) / 1000
	usedK := inputTokens / 1000
	maxK := maxTokens / 1000

	// Determine level
	level := "ok"
	if ratio >= hardGateThreshold {
		level = "hard_gate"
	} else if ratio >= softGateThreshold {
		level = "soft_gate"
	} else if ratio >= warnThreshold {
		level = "warn"
	}

	role := resolveContextRole()

	if jsonFlag {
		result := contextUsageResult{
			InputTokens:  inputTokens - cacheRead - cacheCreate,
			OutputTokens: outputTokens,
			CacheRead:    cacheRead,
			CacheCreate:  cacheCreate,
			TotalInput:   inputTokens,
			MaxTokens:    maxTokens,
			UsageRatio:   ratio,
			UsagePct:     pct,
			RemainingK:   remainingK,
			Level:        level,
			Role:         role,
			Transcript:   transcriptPath,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	// Human-readable output
	fmt.Printf("\n%s\n\n", style.Bold.Render("Context Window Usage"))

	// Usage bar
	barWidth := 40
	filled := int(ratio * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	// Color based on level
	switch level {
	case "hard_gate":
		fmt.Printf("  %s [%s] %d%%\n", style.Error.Render("CRITICAL"), bar, pct)
	case "soft_gate":
		fmt.Printf("  %s [%s] %d%%\n", style.Warning.Render("HIGH"), bar, pct)
	case "warn":
		fmt.Printf("  %s [%s] %d%%\n", style.Warning.Render("ELEVATED"), bar, pct)
	default:
		fmt.Printf("  %s [%s] %d%%\n", style.Success.Render("OK"), bar, pct)
	}

	fmt.Printf("  %dk / %dk tokens used", usedK, maxK)
	if remainingK > 0 {
		fmt.Printf("  (~%dk remaining)", remainingK)
	}
	fmt.Println()

	if outputTokens > 0 {
		fmt.Printf("  Output: %dk tokens\n", outputTokens/1000)
	}
	if cacheRead > 0 || cacheCreate > 0 {
		fmt.Printf("  Cache: %dk read, %dk created\n", cacheRead/1000, cacheCreate/1000)
	}

	fmt.Println()

	// Guidance
	switch level {
	case "hard_gate":
		fmt.Printf("  %s Context nearly exhausted! Hand off immediately:\n", style.Error.Render("⚠"))
		fmt.Printf("    gt handoff -s \"Context exhausted\" -m \"<remaining work>\"\n")
		fmt.Printf("    gt done  (if work is complete)\n")
	case "soft_gate":
		fmt.Printf("  %s Context running low — handoff recommended:\n", style.Warning.Render("⚠"))
		fmt.Printf("    gt handoff -s \"Context budget\" -m \"<what remains>\"\n")
	case "warn":
		fmt.Printf("  %s Context elevated — plan to wrap up soon.\n", style.Warning.Render("!"))
	}

	fmt.Println()
	return nil
}

// resolveContextRole determines the current agent role from environment.
func resolveContextRole() string {
	if role := os.Getenv("GT_ROLE"); role != "" {
		return role
	}
	if os.Getenv("GT_POLECAT") != "" {
		return "polecat"
	}
	if os.Getenv("GT_CREW") != "" {
		return "crew"
	}
	if os.Getenv("GT_MAYOR") != "" {
		return "mayor"
	}
	if os.Getenv("GT_DEACON") != "" {
		return "deacon"
	}
	if os.Getenv("GT_WITNESS") != "" {
		return "witness"
	}
	if os.Getenv("GT_REFINERY") != "" {
		return "refinery"
	}
	return ""
}

// ccTranscriptUsage holds usage from the last assistant message in a transcript.
type ccTranscriptUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// readLatestUsageFromTranscript finds the most recent Claude Code transcript
// for the current working directory and extracts the last assistant message's
// token usage. Returns the usage, the transcript file path, and any error.
func readLatestUsageFromTranscript() (*ccTranscriptUsage, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("getting cwd: %w", err)
	}

	projectDir, err := claudeProjectDir(cwd)
	if err != nil {
		return nil, "", err
	}

	transcriptPath, err := findNewestJSONL(projectDir)
	if err != nil {
		return nil, "", err
	}

	usage, err := extractLastUsage(transcriptPath)
	if err != nil {
		return nil, "", err
	}

	return usage, transcriptPath, nil
}

// claudeProjectDir returns the Claude Code project directory for the given workDir.
// Matches the path formula in internal/agentlog/claudecode.go.
func claudeProjectDir(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	normalized := filepath.ToSlash(abs)
	if len(normalized) >= 2 && normalized[1] == ':' {
		normalized = normalized[2:]
	}
	hash := strings.ReplaceAll(normalized, "/", "-")
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home: %w", err)
	}
	dir := filepath.Join(home, ".claude", "projects", hash)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("no Claude Code project directory found at %s", dir)
	}
	return dir, nil
}

// findNewestJSONL finds the most recently modified .jsonl file in the directory.
func findNewestJSONL(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading directory %s: %w", dir, err)
	}

	var bestPath string
	var bestTime time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestTime) {
			bestPath = filepath.Join(dir, e.Name())
			bestTime = info.ModTime()
		}
	}
	if bestPath == "" {
		return "", fmt.Errorf("no .jsonl transcript files found in %s", dir)
	}
	return bestPath, nil
}

// extractLastUsage reads the transcript file from the end and finds the last
// assistant message with a usage object.
func extractLastUsage(path string) (*ccTranscriptUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()

	// Read all lines — we need to find the LAST assistant message with usage.
	// For typical transcripts (<50MB), reading all lines is fine.
	var lastUsage *ccTranscriptUsage

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry struct {
			Type    string `json:"type"`
			Message *struct {
				Usage *struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage,omitempty"`
			} `json:"message,omitempty"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Usage != nil {
			u := entry.Message.Usage
			lastUsage = &ccTranscriptUsage{
				InputTokens:              u.InputTokens,
				OutputTokens:             u.OutputTokens,
				CacheReadInputTokens:     u.CacheReadInputTokens,
				CacheCreationInputTokens: u.CacheCreationInputTokens,
			}
		}
	}

	if lastUsage == nil {
		return nil, fmt.Errorf("no assistant messages with usage found in transcript")
	}
	return lastUsage, nil
}

// parseContextInt parses a string to int, returning 0 on error.
func parseContextInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// parseContextFloat parses a string to float64, returning 0 on error.
func parseContextFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
