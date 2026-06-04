package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// proposerConfig is the minimal, READ-ONLY view of mayor/daemon.json that the
// curio-proposer binary needs: just the curio patrol's kill switches. It is a
// deliberately narrow, hand-rolled struct (not internal/daemon's
// DaemonPatrolConfig) so the binary stays free of the daemon package — which
// transitively imports internal/beads, the mutation capability this binary must
// physically lack. Reading a tiny JSON projection keeps the write-incapable
// import graph intact.
type proposerConfig struct {
	Patrols struct {
		Curio struct {
			// Enabled is the LIVE Patrol kill switch (curio.enabled). The
			// proposer reads it only to report isolation — toggling the LLM
			// lane must NOT depend on it.
			Enabled bool `json:"enabled"`
			// LLM gates the Retrospect/LLM lane (curio.llm.enabled). This is the
			// binary's own kill switch: false (or absent) means the proposer is
			// disabled and exits without reading candidates, leaving the live
			// Patrol untouched.
			LLM struct {
				Enabled bool `json:"enabled"`
			} `json:"llm"`
		} `json:"curio"`
	} `json:"patrols"`
}

// llmEnabled reports whether the Retrospect/LLM lane is switched on
// (curio.llm.enabled == true). Absent config or absent key reads as false: the
// lane is OFF by default and must be explicitly enabled, mirroring the live
// Patrol's opt-in default.
func (c proposerConfig) llmEnabled() bool { return c.Patrols.Curio.LLM.Enabled }

// daemonConfigPath returns the mayor/daemon.json path under townRoot. It
// hard-codes the "mayor" segment rather than importing internal/constants only
// to keep this file's intent obvious; the value is identical to
// constants.RoleMayor.
func daemonConfigPath(townRoot string) string {
	return filepath.Join(townRoot, "mayor", "daemon.json")
}

// loadProposerConfig reads and parses the kill-switch projection of
// mayor/daemon.json. A missing file yields a zero config (LLM lane OFF), not an
// error — an unconfigured town simply has the Retrospect lane disabled.
func loadProposerConfig(townRoot string) (proposerConfig, error) {
	var cfg proposerConfig
	data, err := os.ReadFile(daemonConfigPath(townRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
