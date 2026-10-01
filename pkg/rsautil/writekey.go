package rsautil

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// WriteKeyFiles writes the RSA private key to privPath and the corresponding
// public key to privPath+".pub". Parent directories are created as needed.
func WriteKeyFiles(privKey *rsa.PrivateKey, privPath string) error {
	if err := os.MkdirAll(filepath.Dir(privPath), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}

	// Private key — PKCS#1 PEM, mode 0600
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	// Public key — PKIX PEM, mode 0644
	pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	if err := os.WriteFile(privPath+".pub", pubPEM, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	return nil
}
