package docsonly

import "testing"

func TestIsDocsOnly(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  bool
	}{
		{"empty is not docs-only", nil, false},
		{"empty slice is not docs-only", []string{}, false},
		{"blank entries only", []string{"", "  "}, false},
		{"single quality report", []string{".quality/tech-debt-trend.md"}, true},
		{"nested quality report", []string{"sub/.quality/report.json"}, true},
		{"root markdown", []string{"README.md"}, true},
		{"uppercase markdown extension", []string{"CHANGELOG.MD"}, true},
		{"nested markdown", []string{"docs/guide/setup.md"}, true},
		{"multiple docs", []string{".quality/a.md", "README.md", "docs/x.md"}, true},
		{"blank mixed with docs", []string{"", ".quality/a.md"}, true},
		{"single go file", []string{"internal/cmd/done.go"}, false},
		{"docs plus one code file", []string{".quality/a.md", "internal/foo.go"}, false},
		{"go.mod is not docs", []string{"go.mod"}, false},
		{"yaml config is not docs", []string{"gates.yaml"}, false},
		{"quality substring is not a directory", []string{"src/quality.go"}, false},
		{"markdown-ish name without extension", []string{"notes.markdown"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDocsOnly(tt.files); got != tt.want {
				t.Errorf("IsDocsOnly(%v) = %v, want %v", tt.files, got, tt.want)
			}
		})
	}
}
