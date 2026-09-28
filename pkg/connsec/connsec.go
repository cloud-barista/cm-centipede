// Package connsec encrypts and decrypts the sensitive credentials carried by
// inline ConnectionRef sources (ssh/minio/db) and the beetleDb reference
// password. Reference sources that hold only identifiers pass through
// unchanged.
//
// Values are encrypted with AES-256-GCM and prefixed "enc:" (see cryptoutil).
// Encrypt is idempotent — an already-encrypted value is left as-is — so it is
// safe to call on plan build even if the request already carried ciphertext.
// Decrypt passes through any value lacking the "enc:" prefix.
//
// Ciphertext is the shared wire and storage format, and plaintext exists only
// where a credential is about to be used:
//
//   - POST /plans/target takes plaintext (a person types it) and answers with
//     ciphertext, which the caller pastes into POST /migration unchanged.
//   - The plan is stored exactly as it arrived; EncryptPlan's idempotence means
//     nothing is resealed on the way in.
//   - The migration resolvers decrypt one ref at a time, immediately before
//     building a storagex/dbmsx config.
//   - Read-only responses carry neither: MaskPlan replaces the values with
//     "***", because nothing feeds a GET response back into the API.
package connsec

import (
	"strings"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	"github.com/cloud-barista/cm-centipede/pkg/cryptoutil"
)

const encPrefix = "enc:"

// enc encrypts v unless it is empty or already encrypted.
func enc(v string) (string, error) {
	if v == "" || strings.HasPrefix(v, encPrefix) {
		return v, nil
	}
	return cryptoutil.Encrypt(v, cryptoutil.AESKey)
}

// dec decrypts v (passthrough when not "enc:"-prefixed or empty).
func dec(v string) (string, error) {
	if v == "" {
		return v, nil
	}
	return cryptoutil.Decrypt(v, cryptoutil.AESKey)
}

// EncryptRef returns a copy of ref with the sensitive fields of its inline
// sources and db-reference passwords encrypted. Reference sources without
// secrets are returned unchanged.
func EncryptRef(ref commonmodel.ConnectionRef) (commonmodel.ConnectionRef, error) {
	return transformRef(ref, enc)
}

// DecryptRef returns a copy of ref with the sensitive fields decrypted.
func DecryptRef(ref commonmodel.ConnectionRef) (commonmodel.ConnectionRef, error) {
	return transformRef(ref, dec)
}

// transformRef applies fn to every sensitive field of ref, operating on copies
// so the caller's ConnectionRef (and nested pointers) are not mutated.
func transformRef(ref commonmodel.ConnectionRef, fn func(string) (string, error)) (commonmodel.ConnectionRef, error) {
	var err error

	if ref.SSH != nil {
		c := *ref.SSH
		if c.PrivateKey, err = fn(c.PrivateKey); err != nil {
			return ref, err
		}
		ref.SSH = &c
	}

	if ref.Minio != nil {
		c := *ref.Minio
		if c.SecretAccessKey, err = fn(c.SecretAccessKey); err != nil {
			return ref, err
		}
		ref.Minio = &c
	}

	if ref.DB != nil {
		c := *ref.DB
		if c.Password, err = fn(c.Password); err != nil {
			return ref, err
		}
		if c.SSHTunnel != nil {
			t := *c.SSHTunnel
			if t.PrivateKey, err = fn(t.PrivateKey); err != nil {
				return ref, err
			}
			c.SSHTunnel = &t
		}
		ref.DB = &c
	}

	if ref.BeetleDB != nil {
		c := *ref.BeetleDB
		if c.Password, err = fn(c.Password); err != nil {
			return ref, err
		}
		ref.BeetleDB = &c
	}

	return ref, nil
}
