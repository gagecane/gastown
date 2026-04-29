// Claude Code accounts and quota state types. Extracted from types.go.
package config

import (
	"fmt"
	"os"
)

// AccountsConfig represents Claude Code account configuration (mayor/accounts.json).
// This enables Gas Town to manage multiple Claude Code accounts with easy switching.
type AccountsConfig struct {
	Version  int                `json:"version"`  // schema version
	Accounts map[string]Account `json:"accounts"` // handle -> account details
	Default  string             `json:"default"`  // default account handle
}

// Account represents a single Claude Code account.
type Account struct {
	Email       string `json:"email"`                 // account email
	Description string `json:"description,omitempty"` // human description
	ConfigDir   string `json:"config_dir"`            // path to CLAUDE_CONFIG_DIR
}

// CurrentAccountsVersion is the current schema version for AccountsConfig.
const CurrentAccountsVersion = 1

// DefaultAccountsConfigDir returns the default base directory for account configs.
func DefaultAccountsConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return home + "/.claude-accounts", nil
}

// QuotaState represents the quota management state (mayor/quota.json).
// Tracks which accounts are rate-limited and when they were last rotated.
type QuotaState struct {
	Version  int                          `json:"version"`  // schema version
	Accounts map[string]AccountQuotaState `json:"accounts"` // handle -> quota state

	// ActiveSwaps tracks keychain swap mappings from quota rotation.
	// Key: target config dir (where the swapped token was written)
	// Value: source account handle (whose token was swapped in)
	//
	// When a session is rotated, its config dir's keychain entry gets
	// overwritten with the source account's token. If the source account
	// later re-authenticates, the fresh token goes to the source's own
	// keychain entry — not the target's. SyncSwappedTokens uses this map
	// to propagate fresh tokens to all target keychain entries.
	ActiveSwaps map[string]string `json:"active_swaps,omitempty"` // targetConfigDir -> sourceAccountHandle
}

// AccountQuotaStatus is the rate-limit status of an account.
type AccountQuotaStatus string

const (
	// QuotaStatusAvailable means the account is not rate-limited.
	QuotaStatusAvailable AccountQuotaStatus = "available"

	// QuotaStatusLimited means the account has been detected as rate-limited.
	QuotaStatusLimited AccountQuotaStatus = "limited"

	// QuotaStatusCooldown means the account was limited and is in cooldown.
	QuotaStatusCooldown AccountQuotaStatus = "cooldown"
)

// AccountQuotaState tracks the quota status of a single account.
type AccountQuotaState struct {
	Status    AccountQuotaStatus `json:"status"`               // current status
	LimitedAt string             `json:"limited_at,omitempty"` // RFC3339 when limit was detected
	ResetsAt  string             `json:"resets_at,omitempty"`  // Human-readable reset time from provider (e.g. "7pm (America/Los_Angeles)")
	LastUsed  string             `json:"last_used,omitempty"`  // RFC3339 when account was last assigned to a session
}

// CurrentQuotaVersion is the current schema version for QuotaState.
const CurrentQuotaVersion = 1
