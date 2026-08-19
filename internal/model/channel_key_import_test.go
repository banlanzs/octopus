package model

import (
	"reflect"
	"testing"
)

func TestParseChannelKeyImportContent(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantKeys       []string
		wantDuplicates int
	}{
		{
			name:           "one per line with blank lines and quotes",
			content:        "\"sk-a\"\n\n'sk-b'\n`sk-c`\n",
			wantKeys:       []string{"sk-a", "sk-b", "sk-c"},
			wantDuplicates: 0,
		},
		{
			name:           "comma semicolon and tab separators",
			content:        "sk-a, sk-b;sk-c\tsk-d",
			wantKeys:       []string{"sk-a", "sk-b", "sk-c", "sk-d"},
			wantDuplicates: 0,
		},
		{
			name:           "whitespace separated on one line",
			content:        "sk-a sk-b sk-c",
			wantKeys:       []string{"sk-a", "sk-b", "sk-c"},
			wantDuplicates: 0,
		},
		{
			name:           "json string array",
			content:        `["sk-a", "sk-b"]`,
			wantKeys:       []string{"sk-a", "sk-b"},
			wantDuplicates: 0,
		},
		{
			name:           "deduplicate and strip bearer",
			content:        "Bearer sk-a\nbearer sk-a\nsk-a",
			wantKeys:       []string{"sk-a"},
			wantDuplicates: 2,
		},
		{
			name:           "empty input",
			content:        "  \n\t\n",
			wantKeys:       nil,
			wantDuplicates: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, duplicates := ParseChannelKeyImportContent(tt.content)
			if !reflect.DeepEqual(keys, tt.wantKeys) {
				t.Fatalf("keys = %#v, want %#v", keys, tt.wantKeys)
			}
			if duplicates != tt.wantDuplicates {
				t.Fatalf("duplicates = %d, want %d", duplicates, tt.wantDuplicates)
			}
		})
	}
}

func TestNormalizeChannelKeyImportItem(t *testing.T) {
	tests := map[string]string{
		`  "sk-a"  `:  "sk-a",
		`'sk-b'`:      "sk-b",
		"Bearer sk-c": "sk-c",
		"bearer sk-d": "sk-d",
		"BEARER sk-e": "sk-e",
		"sk-f":        "sk-f",
	}
	for input, want := range tests {
		if got := NormalizeChannelKeyImportItem(input); got != want {
			t.Fatalf("NormalizeChannelKeyImportItem(%q) = %q, want %q", input, got, want)
		}
	}
}
