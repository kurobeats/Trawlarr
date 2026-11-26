// Package auth handles password hashing, TOTP and token generation.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Argon2id parameters. OWASP's second recommended configuration.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// MinPasswordLength is the minimum accepted password length.
const MinPasswordLength = 12

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an argon2id or legacy bcrypt hash.
// Imported databases store bcrypt hashes, so migrated accounts keep working.
func VerifyPassword(hash, password string) bool {
	switch {
	case strings.HasPrefix(hash, "$argon2id$"):
		return verifyArgon2id(hash, password)
	case strings.HasPrefix(hash, "$2a$"), strings.HasPrefix(hash, "$2b$"), strings.HasPrefix(hash, "$2y$"):
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
	return false
}

// NeedsRehash reports whether a stored hash should be upgraded on next login.
func NeedsRehash(hash string) bool {
	return !strings.HasPrefix(hash, "$argon2id$")
}

func verifyArgon2id(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 {
		return false
	}
	var memory, iterations uint32
	var threads uint8
	for _, pair := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return false
		}
		n, err := strconv.Atoi(kv[1])
		if err != nil {
			return false
		}
		switch kv[0] {
		case "m":
			memory = uint32(n)
		case "t":
			iterations = uint32(n)
		case "p":
			threads = uint8(n)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns n random bytes as URL-safe base64 without padding.
func NewToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// TOTPSecret returns a fresh base32 secret.
func TOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// TOTPURL builds the otpauth:// URI a authenticator app scans.
func TOTPURL(secret, account, issuer string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=6&period=30",
		issuer, account, secret, issuer)
}

// VerifyTOTP checks a 6 digit code, allowing one period of clock drift.
func VerifyTOTP(secret, code string, at time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, offset := range []int64{-1, 0, 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, at.Unix()/30+offset)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func totpCode(secret string, counter int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (int64(sum[offset])&0x7f)<<24 |
		int64(sum[offset+1])<<16 |
		int64(sum[offset+2])<<8 |
		int64(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}
