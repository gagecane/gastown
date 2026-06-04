//go:build !windows

package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsReaperRemovingErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "removing reaper",
			err:  errors.New(`unexpected container status "removing"`),
			want: true,
		},
		{
			name: "different status",
			err:  errors.New(`unexpected container status "exited"`),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReaperRemovingErr(tt.err); got != tt.want {
				t.Errorf("isReaperRemovingErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsLogWaitTimeoutErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "real timeout from testcontainers wait/log.go",
			// Format from testcontainers-go/wait/log.go checkCount(): %q matched %d times, expected %d
			err:  errors.New(`"Server ready. Accepting connections." matched 0 times, expected 1`),
			want: true,
		},
		{
			name: "matched 0 times for unrelated log",
			err:  errors.New(`"some other line" matched 0 times, expected 1`),
			want: false,
		},
		{
			name: "matched some times, not zero",
			err:  errors.New(`"Server ready. Accepting connections." matched 2 times, expected 1`),
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLogWaitTimeoutErr(tt.err); got != tt.want {
				t.Errorf("isLogWaitTimeoutErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsTransientStartupErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "reaper removing",
			err:  errors.New(`unexpected container status "removing"`),
			want: true,
		},
		{
			name: "log wait timeout",
			err:  errors.New(`"Server ready. Accepting connections." matched 0 times, expected 1`),
			want: true,
		},
		{
			name: "permanent failure",
			err:  errors.New("image not found"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientStartupErr(tt.err); got != tt.want {
				t.Errorf("isTransientStartupErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsDockerUnavailableErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "rootless", err: errors.New("testcontainers docker unavailable: rootless Docker not found"), want: true},
		{name: "daemon", err: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock"), want: true},
		{name: "ordinary", err: errors.New("pulling image failed"), want: false},
		// Registry-unreachable network flakes — daemon is up but can't reach
		// Docker Hub. The "client.Timeout" case is the exact error from CI run
		// 26873990005 that reddened the refinery nightly.
		{
			name: "registry pull timeout",
			err:  errors.New(`starting Dolt container: run dolt: generic container: create container: Error response from daemon: Get "https://registry-1.docker.io/v2/": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)`),
			want: true,
		},
		{name: "tls handshake timeout", err: errors.New(`Get "https://registry-1.docker.io/v2/": net/http: TLS handshake timeout`), want: true},
		{name: "dns failure", err: errors.New(`dial tcp: lookup registry-1.docker.io: no such host`), want: true},
		// A genuine bad image tag must STILL fail the build, not skip.
		{name: "manifest not found", err: errors.New(`Error response from daemon: manifest for dolthub/dolt-sql-server:9.9.9 not found`), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDockerUnavailableErr(tt.err); got != tt.want {
				t.Fatalf("isDockerUnavailableErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestCleanTestCircuitBreakerFilesIn verifies the gu-9ynqw teardown: it removes
// circuit-breaker files for ephemeral test DBs (testdb_/beads_t/beads_pt/
// doctest_) but leaves production/rig files (hq, a real rig) and non-circuit
// files untouched.
func TestCleanTestCircuitBreakerFilesIn(t *testing.T) {
	dir := t.TempDir()
	files := map[string]bool{ // name -> shouldBeRemoved
		"beads-dolt-circuit-127-0-0-1-3307-testdb_abc123.json":    true,
		"beads-dolt-circuit-127-0-0-1-3307-beads_test99.json":     true,
		"beads-dolt-circuit-127-0-0-1-3307-beads_pt_x.json":       true,
		"beads-dolt-circuit-127-0-0-1-3307-doctest_foo.json":      true,
		"beads-dolt-circuit-127-0-0-1-3307-hq.json":               false, // production HQ — keep
		"beads-dolt-circuit-127-0-0-1-3307-gastown_upstream.json": false, // real rig — keep
		"some-other-file.json":                                    false, // not a circuit file
		"beads-dolt-circuit-127-0-0-1-3307-talontriage.txt":       false, // not .json
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cleanTestCircuitBreakerFilesIn(dir)

	for name, shouldRemove := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		removed := errors.Is(err, os.ErrNotExist)
		if removed != shouldRemove {
			t.Errorf("%s: removed=%v, want removed=%v", name, removed, shouldRemove)
		}
	}

	// Missing dir must be a no-op, not a panic/error.
	cleanTestCircuitBreakerFilesIn(filepath.Join(dir, "does-not-exist"))
}
