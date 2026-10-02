package franklinwh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestAESKeyFrom(t *testing.T) {
	// 32-char MD5 hex: middle 16 chars start at (32-16)/2 = 8.
	const hash = "0123456789abcdef0123456789abcdef"
	got := string(aesKeyFrom(hash))
	want := hash[8:24]
	if got != want {
		t.Fatalf("aesKeyFrom(32-char) = %q, want %q", got, want)
	}
	if len(got) != 16 {
		t.Fatalf("key length = %d, want 16", len(got))
	}
	// Short strings are returned unchanged.
	if s := string(aesKeyFrom("short")); s != "short" {
		t.Fatalf("aesKeyFrom(short) = %q, want %q", s, "short")
	}
}

// TestEncryptPassword decrypts the output and checks it is the MD5 hash of
// the password, encrypted with the middle-16 key — i.e. exactly what the
// server expects.
func TestEncryptPassword(t *testing.T) {
	const password = "hunter2password!"
	enc, err := encryptPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(enc, ":")
	if len(parts) != 2 {
		t.Fatalf("expected base64ct:base64iv, got %q", enc)
	}
	ct, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("ciphertext base64: %v", err)
	}
	iv, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("iv base64: %v", err)
	}
	if len(iv) != aes.BlockSize {
		t.Fatalf("iv length = %d, want %d", len(iv), aes.BlockSize)
	}

	sum := md5.Sum([]byte(password))
	hexHash := hex.EncodeToString(sum[:])
	block, err := aes.NewCipher(aesKeyFrom(hexHash))
	if err != nil {
		t.Fatal(err)
	}
	if len(ct)%aes.BlockSize != 0 {
		t.Fatalf("ciphertext length %d not a block multiple", len(ct))
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pt, err = pkcs7Unpad(pt)
	if err != nil {
		t.Fatalf("unpad: %v", err)
	}
	if string(pt) != hexHash {
		t.Fatalf("decrypted = %q, want MD5 hash %q", pt, hexHash)
	}
}

// TestEncryptPasswordRandomIV checks two encryptions differ (random IV).
func TestEncryptPasswordRandomIV(t *testing.T) {
	a, err := encryptPassword("same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := encryptPassword("same")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two encryptions produced identical output; IV is not random")
	}
}

func TestPKCS7PadUnpad(t *testing.T) {
	for _, in := range []string{"", "a", "0123456789abcdef", "0123456789abcdef0"} {
		padded := pkcs7Pad([]byte(in), aes.BlockSize)
		if len(padded)%aes.BlockSize != 0 {
			t.Fatalf("pad(%q) length %d not a block multiple", in, len(padded))
		}
		out, err := pkcs7Unpad(padded)
		if err != nil {
			t.Fatalf("unpad(%q): %v", in, err)
		}
		if string(out) != in {
			t.Fatalf("roundtrip(%q) = %q", in, out)
		}
	}
}

func TestNewClientID(t *testing.T) {
	id := NewClientID()
	if len(id) != 36 {
		t.Fatalf("client id %q length %d, want 36", id, len(id))
	}
	if strings.Count(id, "-") != 4 {
		t.Fatalf("client id %q should have 4 dashes", id)
	}
	if id == NewClientID() {
		t.Fatal("client IDs should be unique")
	}
}

// pkcs7Unpad is a test helper that validates and removes PKCS#7 padding.
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errBadPadding
	}
	n := int(data[len(data)-1])
	if n == 0 || n > aes.BlockSize || n > len(data) {
		return nil, errBadPadding
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errBadPadding
		}
	}
	return data[:len(data)-n], nil
}

var errBadPadding = &padError{}

type padError struct{}

func (*padError) Error() string { return "bad PKCS#7 padding" }
