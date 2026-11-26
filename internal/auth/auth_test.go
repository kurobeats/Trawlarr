package auth

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(hash, "wrong password here") {
		t.Fatal("invalid password accepted")
	}
	if NeedsRehash(hash) {
		t.Fatal("fresh hash should not need rehash")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected policy error for short password")
	}
}

// Accounts migrated from another database store bcrypt hashes and keep working.
func TestLegacyBcrypt(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("legacy-bcrypt-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(string(hash), "legacy-bcrypt-password") {
		t.Fatal("bcrypt password rejected")
	}
	if !NeedsRehash(string(hash)) {
		t.Fatal("bcrypt hash should be upgraded on login")
	}
}

// TOTP values from RFC 6238 appendix B, truncated to the six digits we use.
func TestTOTPRFCVectors(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // "12345678901234567890"
	cases := []struct {
		unix int64
		code string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		at := time.Unix(c.unix, 0)
		if !VerifyTOTP(secret, c.code, at) {
			t.Errorf("code %s rejected at %d", c.code, c.unix)
		}
		if VerifyTOTP(secret, "000000", at) && c.code != "000000" {
			t.Errorf("wrong code accepted at %d", c.unix)
		}
	}
}

func TestTOTPToleratesOneStep(t *testing.T) {
	secret := TOTPSecret()
	now := time.Unix(1700000000, 0)
	code := totpCode(secret, now.Unix()/30)
	if !VerifyTOTP(secret, code, now.Add(30*time.Second)) {
		t.Fatal("code rejected outside the current step")
	}
	if VerifyTOTP(secret, code, now.Add(5*time.Minute)) {
		t.Fatal("stale code accepted")
	}
}
