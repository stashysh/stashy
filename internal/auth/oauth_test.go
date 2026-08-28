package auth

import "testing"

func TestSafeRedirectPath(t *testing.T) {
	cases := []struct {
		name string
		next string
		want string
	}{
		{"relative path", "/files", "/files"},
		{"relative path with query", "/abc123/name?download=1", "/abc123/name?download=1"},
		{"empty", "", ""},
		{"relative without slash", "files", ""},
		{"scheme URL", "https://example.com/files", ""},
		{"scheme-relative URL", "//example.com/files", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeRedirectPath(tc.next); got != tc.want {
				t.Fatalf("safeRedirectPath(%q) = %q, want %q", tc.next, got, tc.want)
			}
		})
	}
}
