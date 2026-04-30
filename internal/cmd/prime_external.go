package cmd

// External tool runners invoked during `gt prime`.
//
// These helpers shell out to `bd prime` and `gt mail check --inject`, and read
// agent memories out of beads kv so their output is injected into the agent's
// priming context. All failures are non-fatal — prime continues even if these
// tools are missing or misbehave.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// runPrimeExternalTools runs bd prime, memory injection, and gt mail check --inject.
// Skipped in dry-run mode with explain output.
func runPrimeExternalTools(cwd string) {
	if primeDryRun {
		explain(true, "bd prime: skipped in dry-run mode")
		explain(true, "memory injection: skipped in dry-run mode")
		explain(true, "gt mail check --inject: skipped in dry-run mode")
		return
	}
	runBdPrime(cwd)
	runMemoryInject()
	runMailCheckInject(cwd)
}

// runBdPrime runs `bd prime` and outputs the result.
// This provides beads workflow context to the agent.
func runBdPrime(workDir string) {
	cmd := exec.Command("bd", "prime")
	cmd.Dir = workDir
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Skip if bd prime fails (beads might not be available)
		// But log stderr if present for debugging
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			fmt.Fprintf(os.Stderr, "bd prime: %s\n", errMsg)
		}
		return
	}

	output := strings.TrimSpace(stdout.String())
	if output != "" {
		fmt.Println()
		fmt.Println(output)
	}
}

// memoryTypeLabels maps type keys to human-readable section headers for prime injection.
var memoryTypeLabels = map[string]string{
	"feedback":  "Behavioral Rules (from user feedback)",
	"user":      "User Context",
	"project":   "Project Context",
	"reference": "Reference Links",
	"general":   "General",
}

// runMemoryInject loads memories from beads kv and outputs them during prime.
// Memories are grouped by type and ordered by priority (feedback first).
func runMemoryInject() {
	kvs, err := bdKvListJSON()
	if err != nil {
		return // Silently skip if kv list fails
	}

	// Group memories by type
	type mem struct {
		shortKey string
		value    string
	}
	grouped := make(map[string][]mem)

	for k, v := range kvs {
		if !strings.HasPrefix(k, memoryKeyPrefix) {
			continue
		}
		memType, shortKey := parseMemoryKey(k)
		grouped[memType] = append(grouped[memType], mem{shortKey: shortKey, value: v})
	}

	if len(grouped) == 0 {
		return
	}

	// Sort each group by key
	for t := range grouped {
		sort.Slice(grouped[t], func(i, j int) bool {
			return grouped[t][i].shortKey < grouped[t][j].shortKey
		})
	}

	fmt.Println()
	fmt.Println("# Agent Memories")

	for _, t := range memoryTypeOrder {
		mems, ok := grouped[t]
		if !ok || len(mems) == 0 {
			continue
		}
		label := memoryTypeLabels[t]
		if label == "" {
			label = t
		}
		fmt.Printf("\n## %s\n\n", label)
		for _, m := range mems {
			fmt.Printf("- **%s**: %s\n", m.shortKey, m.value)
		}
	}
}

// runMailCheckInject runs `gt mail check --inject` and outputs the result.
// This injects any pending mail into the agent's context.
func runMailCheckInject(workDir string) {
	cmd := exec.Command("gt", "mail", "check", "--inject")
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Skip if mail check fails, but log stderr for debugging
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			fmt.Fprintf(os.Stderr, "gt mail check: %s\n", errMsg)
		}
		return
	}

	output := strings.TrimSpace(stdout.String())
	if output != "" {
		fmt.Println()
		fmt.Println(output)
	}
}
