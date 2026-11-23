package ote

import "testing"

func TestRemoveAllowlistEntry(t *testing.T) {
	const entry = "^test-rule$"
	for _, tc := range []struct {
		name, current, want string
		wantConflict        bool
	}{
		{name: "restore empty policy", current: "\n" + entry, want: ""},
		{name: "preserve trailing newline", current: "original\n\n" + entry, want: "original\n"},
		{name: "preserve concurrent edits", current: "edited\n" + entry + "\nadded\n", want: "edited\nadded\n"},
		{name: "already removed", current: "admin-rule", want: "admin-rule"},
		{name: "ambiguous duplicate", current: entry + "\n" + entry, want: entry + "\n" + entry, wantConflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := removeAllowlistEntry(tc.current, entry)
			if got != tc.want || (err != nil) != tc.wantConflict {
				t.Errorf("removeAllowlistEntry() = %q, %v; want %q, conflict=%t", got, err, tc.want, tc.wantConflict)
			}
		})
	}
}
