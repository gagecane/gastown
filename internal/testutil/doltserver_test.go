//go:build !windows

package testutil

import (
	"errors"
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
