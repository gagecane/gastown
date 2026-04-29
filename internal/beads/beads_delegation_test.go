package beads

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDelegationJSONOmitEmpty verifies that optional fields (terms, created_at)
// are omitted from JSON when empty. Delegation metadata is stored inside the
// issue metadata JSON, so keeping it compact matters. Complements
// TestDelegationStruct in beads_test.go, which tests full serialization.
func TestDelegationJSONOmitEmpty(t *testing.T) {
	d := Delegation{
		Parent:      "gu-parent",
		Child:       "gu-child",
		DelegatedBy: "mayor",
		DelegatedTo: "polecat",
	}
	data, err := json.Marshal(&d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if strings.Contains(got, `"terms"`) {
		t.Errorf("empty Terms should be omitted: %s", got)
	}
	if strings.Contains(got, `"created_at"`) {
		t.Errorf("empty CreatedAt should be omitted: %s", got)
	}
}

// TestAddDelegation_ValidationErrors covers the pre-storage validation in
// AddDelegation. These errors are returned before any CLI/store calls, so
// the test does not need a mock bd.
func TestAddDelegation_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		d       *Delegation
		wantErr string
	}{
		{
			name:    "missing parent",
			d:       &Delegation{Child: "c", DelegatedBy: "by", DelegatedTo: "to"},
			wantErr: "parent and child",
		},
		{
			name:    "missing child",
			d:       &Delegation{Parent: "p", DelegatedBy: "by", DelegatedTo: "to"},
			wantErr: "parent and child",
		},
		{
			name:    "missing both parent and child",
			d:       &Delegation{DelegatedBy: "by", DelegatedTo: "to"},
			wantErr: "parent and child",
		},
		{
			name:    "missing delegated_by",
			d:       &Delegation{Parent: "p", Child: "c", DelegatedTo: "to"},
			wantErr: "delegated_by and delegated_to",
		},
		{
			name:    "missing delegated_to",
			d:       &Delegation{Parent: "p", Child: "c", DelegatedBy: "by"},
			wantErr: "delegated_by and delegated_to",
		},
	}

	b := NewIsolated(t.TempDir())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := b.AddDelegation(tt.d)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestParseDelegationFromMetadata_EmptyMetadata verifies that an empty or
// nil metadata field yields (nil, nil) — no delegation, no error.
func TestParseDelegationFromMetadata_EmptyMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"nil raw", nil},
		{"empty raw", json.RawMessage{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseDelegationFromMetadata(tt.metadata)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != nil {
				t.Errorf("expected nil delegation, got %+v", d)
			}
		})
	}
}

// TestParseDelegationFromMetadata_NoDelegationKey verifies that metadata
// without a "delegated_from" key yields (nil, nil). Other metadata keys
// must not be affected.
func TestParseDelegationFromMetadata_NoDelegationKey(t *testing.T) {
	meta := json.RawMessage(`{"other_key": "some_value", "another": 42}`)
	d, err := parseDelegationFromMetadata(meta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != nil {
		t.Errorf("expected nil delegation, got %+v", d)
	}
}

// TestParseDelegationFromMetadata_NullValue verifies that an explicit null
// for delegated_from (the way a cleared delegation looks) yields (nil, nil).
// This matches the semantics of RemoveDelegation + storeDelegationClear,
// which may write null during transitions.
func TestParseDelegationFromMetadata_NullValue(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"plain null", json.RawMessage(`{"delegated_from": null}`)},
		{"padded null", json.RawMessage(`{"delegated_from":   null   }`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseDelegationFromMetadata(tt.metadata)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != nil {
				t.Errorf("expected nil delegation for null value, got %+v", d)
			}
		})
	}
}

// TestParseDelegationFromMetadata_MalformedMetadata verifies that garbled
// top-level JSON is treated as "no delegation" rather than propagating the
// error. This matches the existing defensive behavior in the implementation
// and prevents a malformed issue from breaking delegation queries.
func TestParseDelegationFromMetadata_MalformedMetadata(t *testing.T) {
	meta := json.RawMessage(`not actually json {{{`)
	d, err := parseDelegationFromMetadata(meta)
	if err != nil {
		t.Fatalf("expected no error for malformed top-level metadata, got %v", err)
	}
	if d != nil {
		t.Errorf("expected nil delegation, got %+v", d)
	}
}

// TestParseDelegationFromMetadata_MalformedDelegation verifies that when
// the "delegated_from" value exists but is not a valid Delegation object,
// an error is returned. The caller (GetDelegation) should surface this
// because it indicates a data-corruption scenario distinct from "no
// delegation".
func TestParseDelegationFromMetadata_MalformedDelegation(t *testing.T) {
	// delegated_from is a number — not a valid Delegation object.
	meta := json.RawMessage(`{"delegated_from": 42}`)
	d, err := parseDelegationFromMetadata(meta)
	if err == nil {
		t.Fatal("expected error for malformed delegation value, got nil")
	}
	if d != nil {
		t.Errorf("expected nil delegation on error, got %+v", d)
	}
	if !strings.Contains(err.Error(), "parsing delegation") {
		t.Errorf("error message = %q, should contain 'parsing delegation'", err.Error())
	}
}

// TestParseDelegationFromMetadata_ValidDelegation verifies the happy-path
// decode: a well-formed delegation embedded in metadata is returned with
// all fields populated.
func TestParseDelegationFromMetadata_ValidDelegation(t *testing.T) {
	original := &Delegation{
		Parent:      "gu-parent",
		Child:       "gu-child",
		DelegatedBy: "hop://gastown/mayor",
		DelegatedTo: "hop://gastown/polecats/vault",
		Terms: &DelegationTerms{
			Portion:     "tests",
			CreditShare: 25,
		},
		CreatedAt: "2026-04-29T13:53:00Z",
	}

	delegationJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal delegation: %v", err)
	}
	meta, err := json.Marshal(map[string]json.RawMessage{
		"delegated_from": delegationJSON,
		// Extra keys should not interfere.
		"other": json.RawMessage(`"ignored"`),
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}

	got, err := parseDelegationFromMetadata(meta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected delegation, got nil")
	}
	if got.Parent != original.Parent || got.Child != original.Child {
		t.Errorf("parent/child mismatch: got (%s,%s), want (%s,%s)",
			got.Parent, got.Child, original.Parent, original.Child)
	}
	if got.DelegatedBy != original.DelegatedBy || got.DelegatedTo != original.DelegatedTo {
		t.Errorf("delegated_by/to mismatch: got (%s,%s), want (%s,%s)",
			got.DelegatedBy, got.DelegatedTo, original.DelegatedBy, original.DelegatedTo)
	}
	if got.CreatedAt != original.CreatedAt {
		t.Errorf("created_at = %q, want %q", got.CreatedAt, original.CreatedAt)
	}
	if got.Terms == nil {
		t.Fatal("Terms unexpectedly nil")
	}
	if got.Terms.Portion != "tests" || got.Terms.CreditShare != 25 {
		t.Errorf("Terms mismatch: %+v", *got.Terms)
	}
}

// TestDelegationTerms_ZeroValue verifies that DelegationTerms zero-value
// is a valid, empty terms object. Callers rely on being able to create a
// Delegation with no Terms set at all (nil pointer) or with a bare terms
// struct.
func TestDelegationTerms_ZeroValue(t *testing.T) {
	var terms DelegationTerms
	if terms.Portion != "" || terms.Deadline != "" ||
		terms.AcceptanceCriteria != "" || terms.CreditShare != 0 {
		t.Errorf("zero DelegationTerms has non-zero fields: %+v", terms)
	}

	// Marshaling a zero terms struct embedded in a delegation should work
	// and should not panic.
	d := Delegation{
		Parent: "p", Child: "c",
		DelegatedBy: "by", DelegatedTo: "to",
		Terms: &terms,
	}
	if _, err := json.Marshal(&d); err != nil {
		t.Errorf("marshal with zero terms: %v", err)
	}
}

// TestDelegationTerms_OmitEmptyFields verifies that optional Terms fields
// are omitted from JSON when empty, matching the ,omitempty tags.
func TestDelegationTerms_OmitEmptyFields(t *testing.T) {
	terms := DelegationTerms{CreditShare: 100}
	data, err := json.Marshal(&terms)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"credit_share":100`) {
		t.Errorf("expected credit_share=100, got %s", got)
	}
	for _, k := range []string{`"portion"`, `"deadline"`, `"acceptance_criteria"`} {
		if strings.Contains(got, k) {
			t.Errorf("empty field %s should be omitted: %s", k, got)
		}
	}
}
