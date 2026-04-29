// Shared pointer-helper functions used across config default constructors.
// Extracted from types.go so that type-specific files can stay focused on
// their own types without duplicating these one-liners.
package config

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}

// intPtr returns a pointer to the given int value.
func intPtr(v int) *int { return &v }
