package connsec

import (
	"fmt"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
)

// RefVisitor is called once per ConnectionRef in a plan. label names the ref's
// position ("fileSystems[0].dstConnection") so an error can say where it came
// from rather than only what went wrong.
type RefVisitor func(label string, ref *commonmodel.ConnectionRef) error

// ForEachRef walks every source and destination ConnectionRef in a plan, in the
// order the plan lists them.
//
// The ref is passed by pointer: a mutating visitor writes through it, a
// read-only one ignores it. Encryption, masking and validation all want the same
// walk, and one walk is one place to add a unit type when the plan grows one.
func ForEachRef(p *targetmodel.TargetDataMigrationModel, fn RefVisitor) error {
	prop := &p.TargetDataMigrationModel

	for i := range prop.FileSystems {
		if err := fn(fmt.Sprintf("fileSystems[%d].srcConnection", i), &prop.FileSystems[i].SrcConnection); err != nil {
			return err
		}
		if err := fn(fmt.Sprintf("fileSystems[%d].dstConnection", i), &prop.FileSystems[i].DstConnection); err != nil {
			return err
		}
	}
	for i := range prop.ObjectStorages {
		if err := fn(fmt.Sprintf("objectStorages[%d].srcConnection", i), &prop.ObjectStorages[i].SrcConnection); err != nil {
			return err
		}
		if err := fn(fmt.Sprintf("objectStorages[%d].dstConnection", i), &prop.ObjectStorages[i].DstConnection); err != nil {
			return err
		}
	}
	for i := range prop.Databases {
		if err := fn(fmt.Sprintf("databases[%d].srcConnection", i), &prop.Databases[i].SrcConnection); err != nil {
			return err
		}
		if err := fn(fmt.Sprintf("databases[%d].dstConnection", i), &prop.Databases[i].DstConnection); err != nil {
			return err
		}
	}
	return nil
}

// EncryptPlan encrypts the sensitive fields of every ConnectionRef in the plan,
// in place. It is idempotent: a ref that is already encrypted is left alone, so
// a plan that arrived as ciphertext is stored as it arrived rather than resealed
// under a fresh nonce.
func EncryptPlan(p *targetmodel.TargetDataMigrationModel) error {
	return ForEachRef(p, func(label string, ref *commonmodel.ConnectionRef) error {
		out, err := EncryptRef(*ref)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		*ref = out
		return nil
	})
}

// VerifyPlanDecryptable reports whether every encrypted value in the plan can be
// decrypted with this server's key, naming the first ref that cannot.
//
// It is read-only: DecryptRef takes its argument by value and copies each
// sub-struct before writing, so discarding the result leaves the plan untouched.
//
// This is what turns "the migration failed somewhere in the middle" into a 400
// on the request that introduced the problem — a plan issued by another
// instance, or before the passphrase changed, cannot run and should not be
// accepted as though it could.
func VerifyPlanDecryptable(p targetmodel.TargetDataMigrationModel) error {
	return ForEachRef(&p, func(label string, ref *commonmodel.ConnectionRef) error {
		if _, err := DecryptRef(*ref); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	})
}

// maskedValue is what a sensitive field reads as once masked.
const maskedValue = "***"

// MaskRef returns a copy of ref with every non-empty sensitive field replaced by
// "***". An empty field stays empty: whether a connection authenticates with a
// password or a key is not itself a secret, and blanking that distinction would
// make a response harder to read for no gain.
//
// Masking rather than ciphertext is deliberate for read-only responses. Nothing
// feeds a GET response back into the API, so there is nothing to round-trip, and
// a value that cannot be replayed is better than one that can.
func MaskRef(ref commonmodel.ConnectionRef) (commonmodel.ConnectionRef, error) {
	return transformRef(ref, func(v string) (string, error) {
		if v == "" {
			return "", nil
		}
		return maskedValue, nil
	})
}

// MaskPlan masks the sensitive fields of every ConnectionRef in the plan, in
// place. Call it on a plan that is on its way into a response, never on one on
// its way into the database.
func MaskPlan(p *targetmodel.TargetDataMigrationModel) error {
	return ForEachRef(p, func(label string, ref *commonmodel.ConnectionRef) error {
		out, err := MaskRef(*ref)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		*ref = out
		return nil
	})
}
