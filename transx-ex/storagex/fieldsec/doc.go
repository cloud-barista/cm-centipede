// Package fieldsec encrypts selected fields of a Go struct in place, so a
// migration model can carry credentials without exposing them wholesale.
//
// Fields are named by dot-separated JSON path — "source.ssh.privateKey", say —
// so the caller picks exactly what is sensitive and the rest of the model stays
// readable. Encrypt returns a struct of the same type with those paths replaced
// by ciphertext, and records the key id at a path of the caller's choosing;
// Decrypt reverses it, given the private key directly, a KeyPair, or a KeyStore
// to look the id up in.
//
// Each value is encrypted on its own under a fresh AES-256-GCM key, which is
// itself wrapped with RSA-OAEP. That hybrid scheme keeps the RSA key out of the
// data path, so a field of any size costs one public-key operation.
package fieldsec
