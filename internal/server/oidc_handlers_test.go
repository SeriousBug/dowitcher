package server

import "testing"

func TestSafeReturnPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/settings", "/settings"},
		{"/authorize?client_id=x", "/authorize?client_id=x"},
		// Off-site redirects, however they are dressed up.
		{"https://evil.example", "/"},
		{"//evil.example", "/"},
		{"/\\evil.example", "/"},
		{"/\r\nLocation: https://evil.example", "/"},
		{"javascript:alert(1)", "/"},
	}
	for _, c := range cases {
		if got := safeReturnPath(c.in); got != c.want {
			t.Errorf("safeReturnPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
