package db

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/cloud-barista/cm-centipede/pkg/cryptoutil"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Meta is an internal key/value table for deployment state that has to outlive a
// restart but is never exposed through the API: the encryption salt and the key
// canary. It is deliberately not a REST model — nothing here is ever serialised
// into a response.
type Meta struct {
	Key   string `gorm:"primaryKey;column:meta_key"`
	Value string `gorm:"column:meta_value"`
}

// TableName pins the table name. The columns are meta_key/meta_value rather than
// key/value so no driver has to decide whether "key" needs quoting.
func (Meta) TableName() string { return "meta" }

const (
	metaEncryptionSalt   = "encryption_salt"
	metaEncryptionCanary = "encryption_canary"

	// canaryPlaintext is encrypted under the current key on first start and
	// decrypted on every start after that. Its content carries no meaning; only
	// whether it still round-trips does.
	canaryPlaintext = "cm-centipede"

	// saltLen is the scrypt salt length. 32 bytes matches the derived key.
	saltLen = 32
)

// getMeta reads one meta row. The bool reports whether the row exists.
func getMeta(key string) (string, bool, error) {
	var m Meta
	err := DB.First(&m, "meta_key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read meta %q: %w", key, err)
	}
	return m.Value, true, nil
}

// setMeta writes one meta row, replacing any existing value.
func setMeta(key, value string) error {
	if err := DB.Save(&Meta{Key: key, Value: value}).Error; err != nil {
		return fmt.Errorf("write meta %q: %w", key, err)
	}
	return nil
}

// EncryptionSalt returns this deployment's scrypt salt, generating it the first
// time the database is used.
//
// The salt is not a secret — it lives in the same file as the data it protects.
// Its job is to make the derived key specific to this deployment, so that a
// precomputed table built against a shared salt is worthless here.
//
// It must never change: the key is derived from (passphrase, salt), so a new
// salt is a new key and every stored credential becomes unreadable. That is also
// why it is stored with the data rather than in configuration — a database
// restored from backup brings its own salt with it.
func EncryptionSalt() ([]byte, error) {
	stored, ok, err := getMeta(metaEncryptionSalt)
	if err != nil {
		return nil, err
	}
	if ok {
		salt, err := base64.StdEncoding.DecodeString(stored)
		if err != nil || len(salt) != saltLen {
			return nil, fmt.Errorf(
				"the encryption salt stored in this database is corrupt (%d bytes after decoding, want %d); "+
					"credentials encrypted under it cannot be recovered", len(salt), saltLen)
		}
		return salt, nil
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate encryption salt: %w", err)
	}
	if err := setMeta(metaEncryptionSalt, base64.StdEncoding.EncodeToString(salt)); err != nil {
		return nil, err
	}
	log.Info().Msg("encryption salt generated for this database")
	return salt, nil
}

// VerifyEncryptionCanary checks that the key derived at startup is the one this
// database was encrypted with, and records the canary on first use.
//
// Without it a changed passphrase starts cleanly and fails later, once per
// migration, as a scatter of unrelated-looking decrypt errors. Here it is one
// message at startup, before anything runs.
func VerifyEncryptionCanary() error {
	stored, ok, err := getMeta(metaEncryptionCanary)
	if err != nil {
		return err
	}

	if !ok {
		sealed, err := cryptoutil.Encrypt(canaryPlaintext, cryptoutil.AESKey)
		if err != nil {
			return fmt.Errorf("seal encryption canary: %w", err)
		}
		return setMeta(metaEncryptionCanary, sealed)
	}

	got, err := cryptoutil.Decrypt(stored, cryptoutil.AESKey)
	if err != nil || got != canaryPlaintext {
		return errors.New(
			"encryption.secretKey does not match the key this database was encrypted with; " +
				"every stored credential is unreadable under the current passphrase. " +
				"Restore the original passphrase, or delete the database file to start over")
	}
	return nil
}
