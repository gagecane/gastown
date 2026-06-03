package sling

import "testing"

func TestConvoyInfo_IsOwnedDirect(t *testing.T) {
	cases := []struct {
		name string
		info *ConvoyInfo
		want bool
	}{
		{"nil receiver", nil, false},
		{"owned + direct", &ConvoyInfo{Owned: true, MergeStrategy: "direct"}, true},
		{"owned + mr", &ConvoyInfo{Owned: true, MergeStrategy: "mr"}, false},
		{"not owned + direct", &ConvoyInfo{Owned: false, MergeStrategy: "direct"}, false},
		{"not owned + empty", &ConvoyInfo{Owned: false, MergeStrategy: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.IsOwnedDirect(); got != tc.want {
				t.Errorf("IsOwnedDirect() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMergeFromFields(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want string
	}{
		{"local strategy", "owner: mayor/\nbase_branch: proto/v3-build\nmerge: local\n", "local"},
		{"mr strategy", "owner: mayor/\nmerge: mr\n", "mr"},
		{"unset", "owner: mayor/\n", ""},
		{"empty desc", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeFromFields(tc.desc); got != tc.want {
				t.Errorf("MergeFromFields(%q) = %q, want %q", tc.desc, got, tc.want)
			}
		})
	}
}

func TestBaseFromFields(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want string
	}{
		{"named relay base", "owner: mayor/\nbase_branch: proto/v3-build\nmerge: local\n", "proto/v3-build"},
		{"no base", "owner: mayor/\nmerge: mr\n", ""},
		{"empty desc", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BaseFromFields(tc.desc); got != tc.want {
				t.Errorf("BaseFromFields(%q) = %q, want %q", tc.desc, got, tc.want)
			}
		})
	}
}

func TestGenerateShortID(t *testing.T) {
	id := GenerateShortID()
	if len(id) != 5 {
		t.Errorf("GenerateShortID() length = %d, want 5 (id=%q)", len(id), id)
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')) {
			t.Errorf("GenerateShortID() = %q contains non-base32-lowercase rune %q", id, r)
		}
	}
	// Two successive IDs should differ (probabilistically).
	if GenerateShortID() == GenerateShortID() {
		t.Error("GenerateShortID() returned identical IDs on successive calls")
	}
}
