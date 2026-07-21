package auth

import "testing"

// TestHashTokenIsDeterministic: the same secret always hashes the same, or a
// second presentation of a valid OAuth token would fail to authenticate.
func TestHashTokenIsDeterministic(t *testing.T) {
	if HashToken("abc") != HashToken("abc") {
		t.Error("hash is not stable across calls")
	}
	if HashToken("abc") == HashToken("abd") {
		t.Error("distinct secrets collide")
	}
}

// TestRandTokenIsUniqueAndSized: two mints differ, and the token carries real
// entropy rather than coming back empty.
func TestRandTokenIsUniqueAndSized(t *testing.T) {
	a := RandToken(32)
	b := RandToken(32)
	if a == b {
		t.Error("two mints collided")
	}
	// 32 bytes base64url (no padding) is 43 chars.
	if len(a) != 43 {
		t.Errorf("token length = %d, want 43", len(a))
	}
}
