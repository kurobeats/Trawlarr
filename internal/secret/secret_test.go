package secret

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "hunter2", strings.Repeat("x", 4096), "unicode ✓ 日本語"} {
		sealed, err := box.Seal([]byte(plain))
		if err != nil {
			t.Fatalf("seal %q: %v", plain, err)
		}
		if plain != "" && !strings.HasPrefix(sealed, prefix) {
			t.Fatalf("sealed value missing version prefix: %q", sealed)
		}
		got, err := box.Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(got) != plain {
			t.Fatalf("round trip mismatch: %q != %q", got, plain)
		}
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	box, _ := New(make([]byte, 32))
	a, _ := box.Seal([]byte("same"))
	b, _ := box.Seal([]byte("same"))
	if a == b {
		t.Fatal("two seals of the same plaintext are identical; nonce reuse")
	}
}

func TestKeyLengthIsEnforced(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Fatalf("%d-byte key accepted", n)
		}
	}
}

func TestWrongKeyFails(t *testing.T) {
	one, _ := New(bytes.Repeat([]byte{1}, 32))
	two, _ := New(bytes.Repeat([]byte{2}, 32))
	sealed, _ := one.Seal([]byte("secret"))
	if _, err := two.Open(sealed); err == nil {
		t.Fatal("ciphertext opened with the wrong key")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	box, _ := New(make([]byte, 32))
	sealed, _ := box.Seal([]byte("secret value"))
	// Flip the last character of the base64 payload.
	last := sealed[len(sealed)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	tampered := sealed[:len(sealed)-1] + string(repl)
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
}

func TestPlaintextPassthrough(t *testing.T) {
	box, _ := New(make([]byte, 32))
	// Imported databases may hold unencrypted values; they still read.
	got, err := box.Open("legacy-plain-password")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "legacy-plain-password" {
		t.Fatalf("legacy value mangled: %q", got)
	}
	if got, err := box.Open(""); err != nil || got != nil {
		t.Fatalf("empty value should decrypt to nil: %q %v", got, err)
	}
}
