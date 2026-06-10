package cmd

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestTransformRefileJSONL_PreservesFieldsAndMintsFreshID(t *testing.T) {
	src := map[string]any{
		"_type":       "issue",
		"id":          "gc-mat",
		"title":       "Refile me",
		"description": "Original desc",
		"status":      "open",
		"priority":    float64(1),
		"issue_type":  "bug",
		"labels":      []any{"bar", "foo"},
		"metadata":    map[string]any{"k": "v"},
		"created_at":  "2026-06-10T23:33:05Z",
		"comments": []any{
			map[string]any{
				"id":         "019eb3e1-e0c9-7082-9589-fc6ab4f138bd",
				"issue_id":   "gc-mat",
				"author":     "alice",
				"text":       "first comment",
				"created_at": "2026-06-10T23:33:05Z",
			},
		},
		"dependencies": []any{
			map[string]any{"issue_id": "gc-mat", "depends_on_id": "gc-5ec", "type": "blocks"},
		},
	}
	line, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, rec, dropped, err := transformRefileJSONL(line)
	if err != nil {
		t.Fatalf("transformRefileJSONL: %v", err)
	}

	if rec.ID != "gc-mat" {
		t.Errorf("rec.ID = %q, want gc-mat", rec.ID)
	}
	if rec.Title != "Refile me" {
		t.Errorf("rec.Title = %q, want %q", rec.Title, "Refile me")
	}
	if rec.Status != "open" {
		t.Errorf("rec.Status = %q, want open", rec.Status)
	}

	if want := []string{"gc-5ec"}; !reflect.DeepEqual(dropped, want) {
		t.Errorf("droppedDeps = %v, want %v", dropped, want)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal transformed: %v", err)
	}

	if _, ok := got["id"]; ok {
		t.Error("transformed record still has an id field; importer must mint a fresh one")
	}
	if _, ok := got["dependencies"]; ok {
		t.Error("transformed record still has dependencies; cross-DB deps must be dropped")
	}

	// Preserved top-level fields.
	for _, key := range []string{"title", "description", "status", "priority", "issue_type", "labels", "metadata", "created_at"} {
		if !reflect.DeepEqual(got[key], src[key]) {
			t.Errorf("field %q = %v, want %v", key, got[key], src[key])
		}
	}

	// Comments preserved but stripped of id/issue_id so they relink cleanly.
	comments, ok := got["comments"].([]any)
	if !ok || len(comments) != 1 {
		t.Fatalf("comments = %v, want one comment", got["comments"])
	}
	comment := comments[0].(map[string]any)
	if _, ok := comment["id"]; ok {
		t.Error("comment still has id; must be stripped to avoid primary key collision")
	}
	if _, ok := comment["issue_id"]; ok {
		t.Error("comment still has issue_id; must be stripped so it relinks to the new bead")
	}
	if comment["text"] != "first comment" {
		t.Errorf("comment text = %v, want %q", comment["text"], "first comment")
	}
	if comment["author"] != "alice" {
		t.Errorf("comment author = %v, want alice", comment["author"])
	}
}

func TestTransformRefileJSONL_NoCommentsNoDeps(t *testing.T) {
	src := map[string]any{
		"id":     "gc-x1",
		"title":  "Bare bead",
		"status": "open",
	}
	line, _ := json.Marshal(src)

	out, rec, dropped, err := transformRefileJSONL(line)
	if err != nil {
		t.Fatalf("transformRefileJSONL: %v", err)
	}
	if rec.ID != "gc-x1" {
		t.Errorf("rec.ID = %q, want gc-x1", rec.ID)
	}
	if len(dropped) != 0 {
		t.Errorf("droppedDeps = %v, want empty", dropped)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["id"]; ok {
		t.Error("id should be removed")
	}
	if got["title"] != "Bare bead" {
		t.Errorf("title = %v, want %q", got["title"], "Bare bead")
	}
}

func TestTransformRefileJSONL_InvalidJSON(t *testing.T) {
	if _, _, _, err := transformRefileJSONL([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestExtractDepTargets(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"not an array", map[string]any{}, nil},
		{
			"two deps",
			[]any{
				map[string]any{"depends_on_id": "gc-1"},
				map[string]any{"depends_on_id": "gc-2"},
			},
			[]string{"gc-1", "gc-2"},
		},
		{
			"skips empty and malformed",
			[]any{
				map[string]any{"depends_on_id": ""},
				map[string]any{"other": "x"},
				"not-an-object",
				map[string]any{"depends_on_id": "gc-3"},
			},
			[]string{"gc-3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractDepTargets(tc.in)
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("extractDepTargets() = %v, want %v", got, want)
			}
		})
	}
}
