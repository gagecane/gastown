package plugin

import "bytes"

// extractJSONObject finds the first '{' byte in data and returns from that
// point onward. This strips any non-JSON prefix (warning messages, notices)
// that `bd` may emit to stdout before the actual JSON object — for example,
// the test-data warning emitted by `bd create --json` when the title looks
// like test data.
//
// Returns the original data unchanged if no '{' is found, so json.Unmarshal
// still produces a meaningful error for genuinely malformed output.
func extractJSONObject(data []byte) []byte {
	idx := bytes.IndexByte(data, '{')
	if idx < 0 {
		return data
	}
	return data[idx:]
}

// extractJSONArray finds the first '[' byte in data and returns from that
// point onward. Mirrors extractJSONObject for array-valued `bd` output
// (e.g. `bd list --json`).
func extractJSONArray(data []byte) []byte {
	idx := bytes.IndexByte(data, '[')
	if idx < 0 {
		return data
	}
	return data[idx:]
}
