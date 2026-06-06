package cmd

import (
	"reflect"
	"testing"
)

func TestStripFlagFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
		want []string
	}{
		{
			name: "space form",
			args: []string{"title", "--repo", "rig", "--type", "bug"},
			flag: "--repo",
			want: []string{"title", "--type", "bug"},
		},
		{
			name: "equals form",
			args: []string{"title", "--repo=rig", "--type", "bug"},
			flag: "--repo",
			want: []string{"title", "--type", "bug"},
		},
		{
			name: "absent",
			args: []string{"title", "--type", "bug"},
			flag: "--repo",
			want: []string{"title", "--type", "bug"},
		},
		{
			name: "leaves sentinel tail intact",
			args: []string{"--repo", "rig", "--", "--repo", "keep"},
			flag: "--repo",
			want: []string{"--", "--repo", "keep"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripFlagFromArgs(tc.args, tc.flag); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("stripFlagFromArgs(%v, %q) = %v, want %v", tc.args, tc.flag, got, tc.want)
			}
		})
	}
}

func TestFlagValueFromArgs(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		names []string
		want  string
	}{
		{
			name:  "space form",
			args:  []string{"title", "--assignee", "rig/crew/x", "--type", "bug"},
			names: []string{"--assignee", "-a"},
			want:  "rig/crew/x",
		},
		{
			name:  "equals form",
			args:  []string{"title", "--assignee=rig/crew/x"},
			names: []string{"--assignee", "-a"},
			want:  "rig/crew/x",
		},
		{
			name:  "short alias",
			args:  []string{"-a", "rig/crew/x"},
			names: []string{"--assignee", "-a"},
			want:  "rig/crew/x",
		},
		{
			name:  "repo flag",
			args:  []string{"title", "--repo", "gastown_upstream"},
			names: []string{"--repo"},
			want:  "gastown_upstream",
		},
		{
			name:  "absent",
			args:  []string{"title", "--type", "bug"},
			names: []string{"--assignee", "-a"},
			want:  "",
		},
		{
			name:  "stops at sentinel",
			args:  []string{"--", "--assignee", "rig/crew/x"},
			names: []string{"--assignee", "-a"},
			want:  "",
		},
		{
			name:  "flag with no value at end",
			args:  []string{"title", "--assignee"},
			names: []string{"--assignee", "-a"},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagValueFromArgs(tc.args, tc.names...); got != tc.want {
				t.Errorf("flagValueFromArgs(%v, %v) = %q, want %q", tc.args, tc.names, got, tc.want)
			}
		})
	}
}
