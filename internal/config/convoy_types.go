// Convoy configuration types. Extracted from types.go.
package config

// ConvoyConfig configures convoy behavior settings.
type ConvoyConfig struct {
	// NotifyOnComplete controls whether convoy completion pushes a notification
	// into the active Mayor session (in addition to mail). Opt-in; default false.
	NotifyOnComplete bool `json:"notify_on_complete,omitempty"`
}
