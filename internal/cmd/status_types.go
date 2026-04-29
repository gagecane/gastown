package cmd

// This file contains the data structures for the `gt status` command.
// Behavior lives in status.go (command/flags), status_gather.go (data
// collection), status_render.go (text/JSON output), status_discover.go
// (agent discovery), status_runtime.go (process inspection), and
// status_watch.go (watch mode).

// TownStatus represents the overall status of the workspace.
type TownStatus struct {
	Name     string         `json:"name"`
	Location string         `json:"location"`
	Overseer *OverseerInfo  `json:"overseer,omitempty"` // Human operator
	DND      *DNDInfo       `json:"dnd,omitempty"`      // Current agent DND status
	Daemon   *ServiceInfo   `json:"daemon,omitempty"`   // Daemon status
	Dolt     *DoltInfo      `json:"dolt,omitempty"`     // Dolt server status
	Tmux     *TmuxInfo      `json:"tmux,omitempty"`     // Tmux server status
	ACP      *ServiceInfo   `json:"acp,omitempty"`      // ACP mayor status
	Agents   []AgentRuntime `json:"agents"`             // Global agents (Mayor, Deacon)
	Rigs     []RigStatus    `json:"rigs"`
	Summary  StatusSum      `json:"summary"`
}

// ServiceInfo represents a background service status.
type ServiceInfo struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
}

// DoltInfo represents the Dolt server status.
type DoltInfo struct {
	Running       bool   `json:"running"`
	PID           int    `json:"pid,omitempty"`
	Port          int    `json:"port"`
	Remote        bool   `json:"remote,omitempty"`
	DataDir       string `json:"data_dir,omitempty"`
	PortConflict  bool   `json:"port_conflict,omitempty"`  // Port taken by another town's Dolt
	ConflictOwner string `json:"conflict_owner,omitempty"` // --data-dir of the process holding the port
}

// TmuxInfo represents the tmux server status.
type TmuxInfo struct {
	Socket       string `json:"socket"`                // Socket name derived from town name (e.g., "gt-test")
	SocketPath   string `json:"socket_path,omitempty"` // Full socket path (e.g., /tmp/tmux-501/gt-test)
	Running      bool   `json:"running"`               // Is the tmux server running?
	PID          int    `json:"pid,omitempty"`         // PID of the tmux server process
	SessionCount int    `json:"session_count"`         // Number of sessions
}

// OverseerInfo represents the human operator's identity and status.
type OverseerInfo struct {
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Username   string `json:"username,omitempty"`
	Source     string `json:"source"`
	UnreadMail int    `json:"unread_mail"`
}

// DNDInfo represents Do Not Disturb status for the current agent context.
type DNDInfo struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
	Agent   string `json:"agent,omitempty"`
}

// AgentRuntime represents the runtime state of an agent.
type AgentRuntime struct {
	Name              string `json:"name"`                         // Display name (e.g., "mayor", "witness")
	Address           string `json:"address"`                      // Full address (e.g., "greenplace/witness")
	Session           string `json:"session"`                      // tmux session name
	Role              string `json:"role"`                         // Role type
	Running           bool   `json:"running"`                      // Is tmux session running?
	ACP               bool   `json:"acp"`                          // Is ACP session active?
	HasWork           bool   `json:"has_work"`                     // Has pinned work?
	WorkTitle         string `json:"work_title,omitempty"`         // Title of pinned work
	HookBead          string `json:"hook_bead,omitempty"`          // Pinned bead ID from agent bead
	State             string `json:"state,omitempty"`              // Agent state from agent bead
	NotificationLevel string `json:"notification_level,omitempty"` // Notification level (verbose, normal, muted)
	UnreadMail        int    `json:"unread_mail"`                  // Number of unread messages
	FirstSubject      string `json:"first_subject,omitempty"`      // Subject of first unread message
	AgentAlias        string `json:"agent_alias,omitempty"`        // Configured agent name (e.g., "opus-46", "pi")
	AgentInfo         string `json:"agent_info,omitempty"`         // Runtime summary (e.g., "claude/opus", "pi/kimi-k2p5")
}

// RigStatus represents status of a single rig.
type RigStatus struct {
	Name         string          `json:"name"`
	Polecats     []string        `json:"polecats"`
	PolecatCount int             `json:"polecat_count"`
	Crews        []string        `json:"crews"`
	CrewCount    int             `json:"crew_count"`
	HasWitness   bool            `json:"has_witness"`
	HasRefinery  bool            `json:"has_refinery"`
	Hooks        []AgentHookInfo `json:"hooks,omitempty"`
	Agents       []AgentRuntime  `json:"agents,omitempty"` // Runtime state of all agents in rig
	MQ           *MQSummary      `json:"mq,omitempty"`     // Merge queue summary
}

// MQSummary represents the merge queue status for a rig.
type MQSummary struct {
	Pending  int    `json:"pending"`   // Open MRs ready to merge (no blockers)
	InFlight int    `json:"in_flight"` // MRs currently being processed
	Blocked  int    `json:"blocked"`   // MRs waiting on dependencies
	State    string `json:"state"`     // idle, processing, or blocked
	Health   string `json:"health"`    // healthy, stale, or empty
}

// AgentHookInfo represents an agent's hook (pinned work) status.
type AgentHookInfo struct {
	Agent    string `json:"agent"`              // Agent address (e.g., "greenplace/toast", "greenplace/witness")
	Role     string `json:"role"`               // Role type (polecat, crew, witness, refinery)
	HasWork  bool   `json:"has_work"`           // Whether agent has pinned work
	Molecule string `json:"molecule,omitempty"` // Attached molecule ID
	Title    string `json:"title,omitempty"`    // Pinned bead title
}

// StatusSum provides summary counts.
type StatusSum struct {
	RigCount      int `json:"rig_count"`
	PolecatCount  int `json:"polecat_count"`
	CrewCount     int `json:"crew_count"`
	WitnessCount  int `json:"witness_count"`
	RefineryCount int `json:"refinery_count"`
	ActiveHooks   int `json:"active_hooks"`
}

// agentDef defines an agent to discover. Used internally by the
// discover* functions to build the list of agents to query in parallel.
type agentDef struct {
	name    string
	address string
	session string
	role    string
	beadID  string
}
