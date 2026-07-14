package secret

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealerRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	sealer, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.Seal([]byte("restic-password"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("restic-password")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	opened, err := sealer.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "restic-password" {
		t.Fatalf("unexpected plaintext %q", opened)
	}
}

func TestParseKey(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	key, err := ParseKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("unexpected key length %d", len(key))
	}
}
