// Namepool configuration types. Extracted from types.go.
package config

// NamepoolConfig represents namepool settings for themed polecat names.
type NamepoolConfig struct {
	// Style picks from a built-in theme (e.g., "mad-max", "minerals", "wasteland").
	// If empty, defaults to "mad-max".
	Style string `json:"style,omitempty"`

	// Names is a custom list of names to use instead of a built-in theme.
	// If provided, overrides the Style setting.
	Names []string `json:"names,omitempty"`

	// MaxBeforeNumbering is when to start appending numbers.
	// Default is 50. After this many polecats, names become name-01, name-02, etc.
	MaxBeforeNumbering int `json:"max_before_numbering,omitempty"`
}

// DefaultNamepoolConfig returns a NamepoolConfig with sensible defaults.
func DefaultNamepoolConfig() *NamepoolConfig {
	return &NamepoolConfig{
		Style:              "mad-max",
		MaxBeforeNumbering: 50,
	}
}
