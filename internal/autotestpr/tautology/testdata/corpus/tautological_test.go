package corpus

// 25 tautological test functions — assertions where the "expected" value
// depends on the function-under-test's return value.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T01: Expected derived from same call's return field
//tautology:yes
func TestTaut01_FieldFromSameReturn(t *testing.T) {
	result := ParseConfig("input.json")
	assert.Equal(t, result.Name, result.Name) // both sides from ParseConfig
}

// T02: Expected derived by accessing field of FUT return
//tautology:yes
func TestTaut02_ExpectedFromFUTField(t *testing.T) {
	cfg := LoadSettings("/path")
	expected := cfg.Timeout
	assert.Equal(t, expected, cfg.Timeout)
}

// T03: Expected built from FUT return via string method
//tautology:yes
func TestTaut03_StringMethodOnReturn(t *testing.T) {
	result := BuildMessage("hello")
	assert.Equal(t, result.String(), result.String())
}

// T04: Both sides from same multi-return function
//tautology:yes
func TestTaut04_MultiReturnSameSource(t *testing.T) {
	name, _ := SplitPath("/usr/local/bin")
	assert.Equal(t, name, name)
}

// T05: Expected from indexing into FUT return slice
//tautology:yes
func TestTaut05_IndexIntoFUTSlice(t *testing.T) {
	items := ListItems("query")
	first := items[0]
	assert.Equal(t, first, items[0])
}

// T06: Expected from FUT return passed through len()
// Note: len() is stdlib-setup, so this tests propagation
//tautology:yes
func TestTaut06_LenOfFUTReturn(t *testing.T) {
	items := ListItems("query")
	count := ListItems("query")
	assert.Equal(t, len(items), len(count))
}

// T07: Type assertion on FUT return used as expected
//tautology:yes
func TestTaut07_TypeAssertOnFUT(t *testing.T) {
	result := GetValue("key")
	str := result.(string)
	assert.Equal(t, str, result.(string))
}

// T08: Expected from FUT via intermediate variable chain
//tautology:yes
func TestTaut08_IntermediateVarChain(t *testing.T) {
	resp := FetchData("url")
	body := resp.Body
	content := body
	assert.Equal(t, content, resp.Body)
}

// T09: Expected from another method on same FUT return object
//tautology:yes
func TestTaut09_MethodOnFUTObject(t *testing.T) {
	obj := CreateObject("test")
	assert.Equal(t, obj.ID(), obj.ID())
}

// T10: Assert.True with comparison of FUT returns
//tautology:yes
func TestTaut10_TrueWithFUTComparison(t *testing.T) {
	a := ComputeHash("data")
	b := ComputeHash("data")
	assert.True(t, a == b) // comparing two calls to same FUT — tainted by same source
}

// T11: Expected from FUT range variable
//tautology:yes
func TestTaut11_RangeOverFUT(t *testing.T) {
	items := GetAll()
	for _, item := range items {
		assert.Equal(t, item.Name, item.Name)
	}
}

// T12: Expected from FUT via selector chain
//tautology:yes
func TestTaut12_SelectorChain(t *testing.T) {
	resp := GetResponse()
	assert.Equal(t, resp.Header.ContentType, resp.Header.ContentType)
}

// T13: Both args tainted — actual from direct call, expected from stored
//tautology:yes
func TestTaut13_StoredVsDirectCall(t *testing.T) {
	stored := Transform("input")
	actual := Transform("input")
	assert.Equal(t, stored, actual)
}

// T14: Expected is FUT return error message
//tautology:yes
func TestTaut14_ErrorMessageFromFUT(t *testing.T) {
	_, err := Validate("bad input")
	require.Error(t, err)
	msg := err.Error()
	assert.Equal(t, msg, err.Error())
}

// T15: Expected from FUT return's sub-struct
//tautology:yes
func TestTaut15_SubStructFromFUT(t *testing.T) {
	result := BuildPlan("spec")
	phase := result.Phases[0]
	assert.Equal(t, phase.Name, result.Phases[0].Name)
}

// T16: Expected from FUT called twice with same args (idempotency "test")
//tautology:yes
func TestTaut16_IdempotencyViaSameCall(t *testing.T) {
	first := Normalize("  hello  ")
	second := Normalize("  hello  ")
	assert.Equal(t, first, second) // both from Normalize — not testing against known good
}

// T17: Expected derived from FUT via map lookup
//tautology:yes
func TestTaut17_MapLookupFromFUT(t *testing.T) {
	m := BuildIndex([]string{"a", "b", "c"})
	val := m["a"]
	assert.Equal(t, val, m["a"])
}

// T18: Expected from FUT through conditional assignment
//tautology:yes
func TestTaut18_ConditionalFromFUT(t *testing.T) {
	result := Process("data")
	var expected string
	if result.OK {
		expected = result.Value
	}
	assert.Equal(t, expected, result.Value)
}

// T19: Assert on length where both sides tainted
//tautology:yes
func TestTaut19_LenBothTainted(t *testing.T) {
	a := Filter(GetItems(), "pred")
	b := Filter(GetItems(), "pred")
	assert.Equal(t, len(a), len(b))
}

// T20: Expected from FUT via append (taint propagates)
//tautology:yes
func TestTaut20_AppendFromFUT(t *testing.T) {
	base := GetDefaults()
	all := GetDefaults()
	assert.Equal(t, len(base), len(all))
}

// T21: Struct literal with FUT field as expected
//tautology:yes
func TestTaut21_StructFieldAsBothSides(t *testing.T) {
	out := Generate("seed")
	assert.Equal(t, out.Hash, out.Hash)
}

// T22: Expected from method call on FUT-returned interface
//tautology:yes
func TestTaut22_InterfaceMethodOnFUT(t *testing.T) {
	svc := NewService("config")
	assert.Equal(t, svc.Status(), svc.Status())
}

// T23: Expected from FUT via slice operation
//tautology:yes
func TestTaut23_SliceOpOnFUT(t *testing.T) {
	data := ReadAll("file")
	chunk := data[:10]
	assert.Equal(t, chunk, data[:10])
}

// T24: FUT return compared to itself via different access paths
//tautology:yes
func TestTaut24_DifferentAccessPaths(t *testing.T) {
	m := ParseManifest("manifest.yaml")
	name1 := m.Entries[0].Name
	name2 := m.Entries[0].Name
	assert.Equal(t, name1, name2)
}

// T25: Expected from FUT with taint through helper that wraps FUT
//tautology:yes
func TestTaut25_WrappedFUT(t *testing.T) {
	raw := Encode("data")
	decoded := Decode(raw)
	reencoded := Encode(decoded)
	assert.Equal(t, raw, reencoded) // both tainted by Encode
}
