package corpus

// 25 well-formed test functions — assertions where the "expected" value
// is independently constructed (literal, table-driven, or computed from
// test input, NOT from the function-under-test return).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// G01: Expected is a string literal
//
//tautology:no
func TestGood01_LiteralExpected(t *testing.T) {
	result := ParseConfig("test.json")
	assert.Equal(t, "myapp", result.Name)
}

// G02: Expected from table-driven test struct
//
//tautology:no
func TestGood02_TableDriven(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"hello", 5},
		{"world", 5},
		{"", 0},
	}
	for _, tt := range tests {
		result := ComputeLength(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

// G03: Expected is a numeric literal
//
//tautology:no
func TestGood03_NumericLiteral(t *testing.T) {
	count := CountItems("bucket")
	assert.Equal(t, 42, count)
}

// G04: Expected from independently constructed fixture
//
//tautology:no
func TestGood04_IndependentFixture(t *testing.T) {
	expected := &Config{Name: "test", Port: 8080}
	actual := LoadSettings("/path/to/config")
	assert.Equal(t, expected.Name, actual.Name)
	assert.Equal(t, expected.Port, actual.Port)
}

// G05: Expected is a boolean literal
//
//tautology:no
func TestGood05_BoolLiteral(t *testing.T) {
	valid := IsValid("good-input")
	assert.True(t, valid)
}

// G06: Expected from test parameter (not FUT return)
//
//tautology:no
func TestGood06_TestParameter(t *testing.T) {
	input := "hello world"
	result := ToUpper(input)
	assert.Equal(t, "HELLO WORLD", result)
}

// G07: Error assertion with nil expected
//
//tautology:no
func TestGood07_NoError(t *testing.T) {
	_, err := Process("valid-input")
	require.NoError(t, err)
}

// G08: Expected from constant
//
//tautology:no
func TestGood08_Constant(t *testing.T) {
	result := GetStatus()
	assert.Equal(t, StatusActive, result)
}

// G09: Expected is empty slice
//
//tautology:no
func TestGood09_EmptySlice(t *testing.T) {
	items := Filter(GetItems(), "nonexistent")
	assert.Empty(t, items)
}

// G10: Assert.Len with literal count
//
//tautology:no
func TestGood10_LenLiteral(t *testing.T) {
	items := ListAll()
	assert.Len(t, items, 3)
}

// G11: Expected from os.Getenv (independent source)
//
//tautology:no
func TestGood11_EnvVar(t *testing.T) {
	t.Setenv("MY_VAR", "expected_value")
	result := ReadEnv("MY_VAR")
	assert.Equal(t, "expected_value", result)
}

// G12: Expected from file content written by test setup
//
//tautology:no
func TestGood12_FileFixture(t *testing.T) {
	dir := t.TempDir()
	WriteFile(dir+"/input.txt", "test content")
	result := ReadFile(dir + "/input.txt")
	assert.Equal(t, "test content", result)
}

// G13: Assert.Contains with literal substring
//
//tautology:no
func TestGood13_ContainsLiteral(t *testing.T) {
	msg := FormatError("missing field")
	assert.Contains(t, msg, "missing field")
}

// G14: Expected from different function (not FUT)
//
//tautology:no
func TestGood14_DifferentFunction(t *testing.T) {
	encoded := Encode("hello")
	decoded := Decode(encoded)
	assert.Equal(t, "hello", decoded) // expected is the input, not from Encode
}

// G15: Assert.NotNil (no expected value comparison)
//
//tautology:no
func TestGood15_NotNil(t *testing.T) {
	obj := CreateObject("test")
	assert.NotNil(t, obj)
}

// G16: Expected from time.Now (independent source)
//
//tautology:no
func TestGood16_TimeComparison(t *testing.T) {
	result := GetTimestamp()
	assert.True(t, result.Year() >= 2020)
}

// G17: Expected from struct field of test input (not FUT output)
//
//tautology:no
func TestGood17_InputFieldAsExpected(t *testing.T) {
	input := &Request{Method: "GET", Path: "/api"}
	result := RouteRequest(input)
	assert.Equal(t, "/api", result.MatchedPath)
}

// G18: Assert.Equal with computed expected from input
//
//tautology:no
func TestGood18_ComputedFromInput(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := JoinStrings(input, ",")
	assert.Equal(t, "a,b,c", result)
}

// G19: Assert on error message with literal
//
//tautology:no
func TestGood19_ErrorMessage(t *testing.T) {
	_, err := Validate("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

// G20: Expected from HTTP status code constant
//
//tautology:no
func TestGood20_HTTPStatus(t *testing.T) {
	resp := MakeRequest("GET", "/health")
	assert.Equal(t, 200, resp.StatusCode)
}

// G21: Table-driven with struct comparison
//
//tautology:no
func TestGood21_TableStructComparison(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantName string
	}{
		{"valid", "good", false, "good"},
		{"empty", "", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseName(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, result)
		})
	}
}

// G22: Assert.False with function result
//
//tautology:no
func TestGood22_FalseLiteral(t *testing.T) {
	result := IsExpired("valid-token")
	assert.False(t, result)
}

// G23: Expected from map literal
//
//tautology:no
func TestGood23_MapLiteral(t *testing.T) {
	expected := map[string]int{"a": 1, "b": 2}
	result := CountChars("aabb")
	assert.Equal(t, expected["a"], result["a"])
	assert.Equal(t, expected["b"], result["b"])
}

// G24: Expected from len of input (not output)
//
//tautology:no
func TestGood24_LenOfInput(t *testing.T) {
	input := []string{"x", "y", "z"}
	result := CopySlice(input)
	assert.Equal(t, len(input), len(result))
}

// G25: Assert.ElementsMatch with independently constructed slice
//
//tautology:no
func TestGood25_ElementsMatch(t *testing.T) {
	result := SortAndDedupe([]string{"b", "a", "b", "c"})
	assert.ElementsMatch(t, []string{"a", "b", "c"}, result)
}
