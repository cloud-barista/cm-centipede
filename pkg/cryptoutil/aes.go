package cryptoutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/scrypt"

	"github.com/cloud-barista/cm-centipede/pkg/config"
)

const encPrefix = "enc:"

// AESKey is the global AES-256 key set by InitKey at startup.
var AESKey []byte

// Encrypt encrypts plaintext with AES-256-GCM and returns "enc:<base64(nonce+ciphertext)>".
// nonce is 12 random bytes prepended to the ciphertext before base64 encoding.
func Encrypt(plaintext string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce, producing nonce||ciphertext||tag.
	blob := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt decrypts a value produced by Encrypt.
// If the value does not start with "enc:" it is returned unchanged (passthrough).
func Decrypt(encrypted string, key []byte) (string, error) {
	if !strings.HasPrefix(encrypted, encPrefix) {
		return encrypted, nil
	}

	blob, err := base64.StdEncoding.DecodeString(encrypted[len(encPrefix):])
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	ns := gcm.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

// minPassphraseLen is a floor, not an entropy policy: it blocks "1234" without
// pretending to measure strength. scrypt is what makes a short passphrase
// expensive to attack.
const minPassphraseLen = 12

// scrypt cost parameters. N=32768 costs roughly 100 ms once at startup — and is
// what an offline attacker pays per guess against a stolen database file.
const (
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
)

// InitKey derives the AES-256 key from config.Conf.Encryption.SecretKey.
//
// The operator picks the value: any passphrase is accepted and run through
// scrypt to produce the 32 bytes AES-256 needs. Nothing is padded and nothing is
// truncated, so nothing the operator typed is silently discarded or silently
// ignored — a key nobody chose is the failure this replaces.
//
// The key is never generated and never written back. A key this server invents
// is a key the operator does not have, and every credential encrypted under it
// dies with the container. An absent value is a startup failure, reported here
// and acted on by the caller.
//
// salt is this deployment's own salt, from db.EncryptionSalt. It is what keeps
// the derived key specific to this database even when two deployments share a
// passphrase.
func InitKey(salt []byte) error {
	v := strings.TrimSpace(config.Conf.Encryption.SecretKey)

	if v == "" {
		return fmt.Errorf(
			"encryption.secretKey is not set — cm-centipede will not start without it.\n" +
				"  Set centipede.encryption.secretKey in conf/cm-centipede.yaml to any\n" +
				"  passphrase you choose, or export CENTIPEDE_ENCRYPTION_SECRET_KEY.\n" +
				"  Keep this value: losing it, or changing it, makes every stored\n" +
				"  credential unrecoverable.")
	}

	if len(v) < minPassphraseLen {
		return fmt.Errorf(
			"encryption.secretKey must be at least %d characters (got %d)", minPassphraseLen, len(v))
	}

	key, err := scrypt.Key([]byte(v), salt, scryptN, scryptR, scryptP, 32)
	if err != nil {
		return fmt.Errorf("derive encryption key: %w", err)
	}
	AESKey = key
	return nil
}
