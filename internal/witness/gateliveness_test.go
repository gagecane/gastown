package witness

import "testing"

func TestGateProcessRunning(t *testing.T) {
	gates := []PolecatGate{
		{Name: "build", Cmd: "go build ./..."},
		{Name: "test", Cmd: "go test ./..."},
		{Name: "rebase-check", Cmd: "scripts/check-upstream-rebased.sh"},
	}

	tests := []struct {
		name     string
		gates    []PolecatGate
		procCmds []string
		want     bool
	}{
		{
			name:     "gate actively running under sh -c",
			gates:    gates,
			procCmds: []string{"sh -c go test ./...", "go test ./...", "/usr/local/go/bin/go test ./..."},
			want:     true,
		},
		{
			name:     "brazil-build release gate running",
			gates:    []PolecatGate{{Name: "build", Cmd: "brazil-build release"}},
			procCmds: []string{"sh -c brazil-build release", "ruby /apollo/.../brazil-build release"},
			want:     true,
		},
		{
			name:     "script gate running",
			gates:    gates,
			procCmds: []string{"/bin/bash scripts/check-upstream-rebased.sh"},
			want:     true,
		},
		{
			name:     "no gate process — only unrelated procs",
			gates:    gates,
			procCmds: []string{"git push origin HEAD", "dolt sql-server", "node /home/x/cli.js"},
			want:     false,
		},
		{
			name:     "no gates configured",
			gates:    nil,
			procCmds: []string{"go test ./..."},
			want:     false,
		},
		{
			name:     "no processes",
			gates:    gates,
			procCmds: nil,
			want:     false,
		},
		{
			name:     "short gate command does not over-match",
			gates:    []PolecatGate{{Name: "noop", Cmd: "ls"}},
			procCmds: []string{"tools_ls_helper --flag"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gateProcessRunning(tt.gates, tt.procCmds); got != tt.want {
				t.Errorf("gateProcessRunning() = %v, want %v", got, tt.want)
			}
		})
	}
}
