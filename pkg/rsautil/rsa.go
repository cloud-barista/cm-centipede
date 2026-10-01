package rsautil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"errors"
	"fmt"
	"os"

	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/rs/zerolog/log"
)

// PrivKey is the global RSA private key loaded at startup.
var PrivKey *rsa.PrivateKey

// GenerateKeyPair generates a 4096-bit RSA key pair.
func GenerateKeyPair() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 4096)
}

// DecryptWithPrivateKey decrypts RSA-OAEP/SHA-512 ciphertext split into 512-byte chunks.
// This is used exclusively to decrypt honeybee API responses.
func DecryptWithPrivateKey(ciphertext []byte, privKey *rsa.PrivateKey) ([]byte, error) {
	const chunkSize = 512

	if len(ciphertext) == 0 {
		return []byte{}, nil
	}
	if len(ciphertext)%chunkSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of %d", len(ciphertext), chunkSize)
	}

	var plaintext []byte
	for i := 0; i < len(ciphertext); i += chunkSize {
		chunk := ciphertext[i : i+chunkSize]
		dec, err := rsa.DecryptOAEP(sha512.New(), rand.Reader, privKey, chunk, nil)
		if err != nil {
			return nil, fmt.Errorf("decrypt chunk %d: %w", i/chunkSize, err)
		}
		plaintext = append(plaintext, dec...)
	}
	return plaintext, nil
}

// InitRSAKey loads the RSA private key from config.Conf.Honeybee.PrivateKeyPath.
// If the key file does not exist a new 4096-bit pair is generated and written to disk.
func InitRSAKey() error {
	path := config.ExpandTilde(config.Conf.Honeybee.PrivateKeyPath)

	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		log.Info().Str("path", path).Msg("RSA key not found, generating new 4096-bit key pair")

		privKey, genErr := GenerateKeyPair()
		if genErr != nil {
			return fmt.Errorf("generate RSA key pair: %w", genErr)
		}
		if writeErr := WriteKeyFiles(privKey, path); writeErr != nil {
			return fmt.Errorf("write RSA key files: %w", writeErr)
		}
		PrivKey = privKey
		log.Info().Str("path", path).Msg("RSA key pair generated and saved")
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat RSA key file: %w", err)
	}

	privKey, err := ReadPrivateKey(path)
	if err != nil {
		return fmt.Errorf("load RSA private key: %w", err)
	}
	PrivKey = privKey
	log.Info().Str("path", path).Msg("RSA private key loaded")
	return nil
}
