package crypto_test

import (
	"bytes"
	"testing"

	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
)

func TestAESGCMRoundTripAndFreshNonce(t *testing.T) {
	c, err := channelcrypto.NewAESGCM(bytes.Repeat([]byte{0x42}, 32))
	if err != nil { t.Fatal(err) }
	plaintext := []byte(`{"access_token":"secret"}`)
	first, err := c.Encrypt(plaintext)
	if err != nil { t.Fatal(err) }
	second, err := c.Encrypt(plaintext)
	if err != nil { t.Fatal(err) }
	if bytes.Equal(first, second) { t.Fatal("cipher reused nonce/ciphertext") }
	decoded, err := c.Decrypt(first)
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(decoded, plaintext) { t.Fatalf("decoded credential differs: %q", decoded) }
}

func TestAESGCMRejectsInvalidKeyAndTampering(t *testing.T) {
	if _, err := channelcrypto.NewAESGCM(make([]byte, 31)); err == nil { t.Fatal("expected invalid key error") }
	c, err := channelcrypto.NewAESGCM(bytes.Repeat([]byte{0x11}, 32))
	if err != nil { t.Fatal(err) }
	ciphertext, err := c.Encrypt([]byte("token"))
	if err != nil { t.Fatal(err) }
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := c.Decrypt(ciphertext); err == nil { t.Fatal("expected tampered ciphertext error") }
}
