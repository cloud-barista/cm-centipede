package base

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// =============================================================================
// TLS Modes
// =============================================================================

// These five are the whole vocabulary dbmsx has for transport security, and
// they mean the same thing on every engine. Two questions separate them: must
// the connection be encrypted, and how far is the server verified.
//
//	mode          encrypt   verify chain   verify host   server offers no TLS
//	----------------------------------------------------------------------------
//	disable       no        -              -             plaintext
//	prefer        try       no             no            plaintext
//	require       yes       no             no            error
//	verify-ca     yes       yes            no            error
//	verify-full   yes       yes            yes           error
//
// Encryption and verification are separate things, and conflating them is the
// usual source of confusion here. A server with require_secure_transport=ON
// demands only the first: it refuses a plaintext connection and does not care
// whether the client checked its certificate. Verification is what the client
// does to protect itself, and it is the half that needs a CA.
//
// What each mode defends against:
//
//	require       a passive listener, but not an active man in the middle -
//	              nothing proves the peer is the server it claims to be
//	prefer        the same, and less: an attacker who strips the server's TLS
//	              capability flag from the unauthenticated handshake makes the
//	              client fall back to plaintext, and the caller cannot tell
//	verify-ca     a man in the middle without the CA's signature, while still
//	              accepting any host that CA vouches for
//	verify-full   both
//
// A server whose certificate chains to a CA the system trust store does not
// carry needs TLSCAFile or TLSCAPEM under verify-ca and verify-full. Without
// one they fail with an unknown-authority error even though the server is
// genuine — that is the client refusing the server, not the other way round.
//
// How each engine spells them:
//
//	mode          MySQL / MariaDB          PostgreSQL           MongoDB
//	----------------------------------------------------------------------------
//	disable       tls=false                sslmode=disable      no TLS
//	prefer        tls=preferred            sslmode=prefer       unsupported
//	require       tls=skip-verify          sslmode=require      InsecureSkipVerify
//	verify-ca     registered config        sslmode=verify-ca    RootCAs, no ServerName
//	verify-full   tls=true, or registered  sslmode=verify-full  RootCAs + ServerName
//
// MongoDB has no prefer: a client either negotiates TLS or it does not, so
// there is nothing for a fallback to describe. It is refused in validation
// rather than quietly promoted to require.
//
// TLS applies to direct connections only. An ssh-tunnel location runs the
// engine's CLI tools on the remote host and this package passes them no TLS
// options, so the fields below have no counterpart in SSHTunnelConfig.
const (
	// TLSModeDisable connects in plaintext. It is the zero value, so a
	// DirectConfig that says nothing about TLS gets none.
	TLSModeDisable = "disable"

	// TLSModePrefer encrypts when the server offers it and silently continues
	// in plaintext when it does not. Use it only where "encrypt if we can" is
	// genuinely the requirement.
	TLSModePrefer = "prefer"

	// TLSModeRequire encrypts and refuses to continue without it, but does not
	// check who the server is. It is the option when the server's CA is not at
	// hand.
	TLSModeRequire = "require"

	// TLSModeVerifyCA additionally checks the certificate chain, but not that
	// the hostname matches - for a database reached through an alias or an
	// endpoint the certificate does not name.
	TLSModeVerifyCA = "verify-ca"

	// TLSModeVerifyFull checks the chain and the hostname. Prefer it whenever
	// the certificate names the host you connect to.
	TLSModeVerifyFull = "verify-full"
)

// TLSModes lists every accepted mode, in increasing order of strictness. It is
// what error messages enumerate, so the order is the one a reader should see.
var TLSModes = []string{
	TLSModeDisable,
	TLSModePrefer,
	TLSModeRequire,
	TLSModeVerifyCA,
	TLSModeVerifyFull,
}

// TLSModeOf returns the mode a config selects, resolving the empty value to
// TLSModeDisable. It does not validate: ValidateTLS does that.
func TLSModeOf(cfg *DirectConfig) string {
	if cfg == nil || cfg.TLSMode == "" {
		return TLSModeDisable
	}
	return cfg.TLSMode
}

// tlsModeVerifies reports whether the mode checks the certificate chain, which
// is the same question as whether a CA is meaningful.
func tlsModeVerifies(mode string) bool {
	return mode == TLSModeVerifyCA || mode == TLSModeVerifyFull
}

// =============================================================================
// Validation
// =============================================================================

// ValidateTLS checks one location's TLS fields on their own. It is called
// before anything connects, so a configuration that cannot work is refused
// while the message can still name the field that is wrong.
//
// dbmsType is needed for one rule only: MongoDB has no prefer.
func ValidateTLS(side, dbmsType string, cfg *DirectConfig) error {
	if cfg == nil {
		return nil
	}

	mode := TLSModeOf(cfg)
	known := false
	for _, m := range TLSModes {
		if mode == m {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%s.tlsMode %q is not supported: use one of %s",
			side, cfg.TLSMode, strings.Join(TLSModes, ", "))
	}

	if cfg.TLSCAFile != "" && cfg.TLSCAPEM != "" {
		return fmt.Errorf("%s.tlsCAFile and %s.tlsCAPem are both set: give one or neither", side, side)
	}

	// A CA is only consulted by the verifying modes. Accepting it elsewhere
	// would let a caller believe the certificate is being checked when the
	// mode says it is not.
	if (cfg.TLSCAFile != "" || cfg.TLSCAPEM != "") && !tlsModeVerifies(mode) {
		return fmt.Errorf(
			"%s specifies a CA but tlsMode is %q, which does not verify the server: "+
				"use %s or %s, or drop the CA",
			side, mode, TLSModeVerifyCA, TLSModeVerifyFull)
	}

	if dbmsType == DBMSTypeMongoDB && mode == TLSModePrefer {
		return fmt.Errorf(
			"%s.tlsMode %q is not supported for MongoDB: a client either negotiates TLS or it does not, "+
				"so there is no plaintext fallback to describe; use %s or %s",
			side, TLSModePrefer, TLSModeDisable, TLSModeRequire)
	}

	// Read the CA now rather than at connect time. A path that does not exist
	// is a configuration mistake, and finding it here means it is reported
	// with the field name instead of as a connection failure much later.
	if _, _, err := loadTLSRoots(cfg); err != nil {
		return fmt.Errorf("%s: %w", side, err)
	}
	return nil
}

// =============================================================================
// Resolution
// =============================================================================

// TLSSettings is the resolved form of a config's TLS fields: the mode, the
// roots to verify against, and the name the certificate must carry.
//
// Every engine goes through ResolveTLS to obtain one, which is what keeps the
// five modes from drifting apart between drivers.
type TLSSettings struct {
	// Mode is one of the TLSMode constants, never empty.
	Mode string

	// RootCAs is nil when no CA was supplied, which means the system trust
	// store. The verifying modes accept that; it simply carries only publicly
	// trusted CAs.
	RootCAs *x509.CertPool

	// ServerName is the host the certificate is checked against under
	// verify-full. Empty for every other mode.
	ServerName string

	// The CA as it was given, kept because a pool cannot be read back:
	// PostgreSQL needs a path (libpq takes no pool) and TLSConfigName needs
	// the bytes to tell two configurations apart.
	caFile  string
	caBytes []byte
}

// ResolveTLS turns a DirectConfig's TLS fields into settings the drivers can
// act on. It repeats the checks ValidateTLS makes, because a driver may be
// reached through a call that never validated a whole migration model.
func ResolveTLS(cfg *DirectConfig) (*TLSSettings, error) {
	if cfg == nil {
		return &TLSSettings{Mode: TLSModeDisable}, nil
	}

	mode := TLSModeOf(cfg)
	roots, pem, err := loadTLSRoots(cfg)
	if err != nil {
		return nil, err
	}

	s := &TLSSettings{
		Mode:    mode,
		RootCAs: roots,
		caFile:  cfg.TLSCAFile,
		caBytes: pem,
	}
	if mode == TLSModeVerifyFull {
		s.ServerName = cfg.Host
	}
	return s, nil
}

// loadTLSRoots builds a pool from whichever CA form was given, returning the
// PEM it was built from as well. Both are nil when no CA was supplied: that is
// the system trust store, not an error.
func loadTLSRoots(cfg *DirectConfig) (*x509.CertPool, []byte, error) {
	var pem []byte
	switch {
	case cfg.TLSCAFile != "":
		b, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read tlsCAFile %q: %w", cfg.TLSCAFile, err)
		}
		pem = b
	case cfg.TLSCAPEM != "":
		pem = []byte(cfg.TLSCAPEM)
	default:
		return nil, nil, nil
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		src := "tlsCAPem"
		if cfg.TLSCAFile != "" {
			src = fmt.Sprintf("tlsCAFile %q", cfg.TLSCAFile)
		}
		return nil, nil, fmt.Errorf("%s contains no PEM certificate", src)
	}
	return pool, pem, nil
}

// TLSConfigName is the name a MySQL-family driver registers this configuration
// under.
//
// go-sql-driver keeps a package-global map keyed by name. Naming an entry after
// the hash of what it contains makes re-registration harmless, keeps two
// different configurations from overwriting each other, and bounds how many
// names can accumulate over the life of the process.
func TLSConfigName(s *TLSSettings) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", s.Mode, s.ServerName)
	h.Write(s.caBytes)
	return "dbmsx-" + hex.EncodeToString(h.Sum(nil)[:8])
}

// HasCustomRoots reports whether a CA was supplied, as opposed to falling back
// to the system trust store.
func (s *TLSSettings) HasCustomRoots() bool { return s != nil && s.RootCAs != nil }

// Enabled reports whether the connection is to be encrypted at all.
func (s *TLSSettings) Enabled() bool { return s != nil && s.Mode != TLSModeDisable }

// =============================================================================
// Go TLS configuration (MySQL, MariaDB, MongoDB)
// =============================================================================

// GoTLSConfig builds the *tls.Config the drivers that take one need. It
// returns nil for disable, which every caller reads as "no TLS".
//
// prefer produces the same config as require: falling back to plaintext is a
// property of the connection attempt, not of the TLS parameters, so the driver
// arranges that part itself.
func (s *TLSSettings) GoTLSConfig() (*tls.Config, error) {
	if s == nil || s.Mode == TLSModeDisable {
		return nil, nil
	}

	switch s.Mode {
	case TLSModePrefer, TLSModeRequire:
		return &tls.Config{InsecureSkipVerify: true}, nil // #nosec G402 - the mode says so

	case TLSModeVerifyFull:
		return &tls.Config{RootCAs: s.RootCAs, ServerName: s.ServerName}, nil

	case TLSModeVerifyCA:
		// verify-ca has no counterpart in crypto/tls: the standard verifier
		// always checks the hostname once it checks the chain. So the built-in
		// path is turned off and the chain is verified here by hand, with an
		// empty DNSName.
		//
		// InsecureSkipVerify reads as "no verification" but is not: the
		// callback below does the work the standard verifier would, minus the
		// hostname.
		roots := s.RootCAs
		return &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 - VerifyPeerCertificate below
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				certs := make([]*x509.Certificate, 0, len(rawCerts))
				for _, raw := range rawCerts {
					c, err := x509.ParseCertificate(raw)
					if err != nil {
						return fmt.Errorf("parse server certificate: %w", err)
					}
					certs = append(certs, c)
				}
				if len(certs) == 0 {
					return errors.New("server presented no certificate")
				}
				opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
				for _, c := range certs[1:] {
					opts.Intermediates.AddCert(c)
				}
				_, err := certs[0].Verify(opts)
				return err
			},
		}, nil
	}
	return nil, fmt.Errorf("tlsMode %q is not supported", s.Mode)
}

// =============================================================================
// PostgreSQL DSN parameters
// =============================================================================

// PgParams returns the two libpq DSN fields PostgreSQL needs, plus a cleanup
// to call when the connection attempt is over.
//
// libpq takes a path rather than a pool, so a CA given as PEM text is written
// to a temporary file for the life of the call. cleanup is never nil.
func (s *TLSSettings) PgParams() (sslmode, sslrootcert string, cleanup func(), err error) {
	noop := func() {}
	if s == nil {
		return TLSModeDisable, "", noop, nil
	}

	// The mode names are libpq's own, so they pass through unchanged. That is
	// deliberate: PostgreSQL is where this vocabulary comes from.
	sslmode = s.Mode

	if !tlsModeVerifies(s.Mode) {
		return sslmode, "", noop, nil
	}
	switch {
	case s.caFile != "":
		return sslmode, s.caFile, noop, nil
	case len(s.caBytes) > 0:
		f, err := os.CreateTemp("", "dbmsx-ca-*.pem")
		if err != nil {
			return "", "", noop, fmt.Errorf("write tlsCAPem to a temporary file: %w", err)
		}
		name := f.Name()
		remove := func() { _ = os.Remove(name) }
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			remove()
			return "", "", noop, fmt.Errorf("secure the temporary CA file: %w", err)
		}
		if _, err := f.Write(s.caBytes); err != nil {
			f.Close()
			remove()
			return "", "", noop, fmt.Errorf("write tlsCAPem to a temporary file: %w", err)
		}
		if err := f.Close(); err != nil {
			remove()
			return "", "", noop, fmt.Errorf("write tlsCAPem to a temporary file: %w", err)
		}
		return sslmode, name, remove, nil
	default:
		// No CA: libpq verifies against its own default locations.
		return sslmode, "", noop, nil
	}
}

// =============================================================================
// Diagnostics
// =============================================================================

// ExplainTLSError adds what to do about a failed certificate check.
//
// The bare error is some form of "unknown authority", which says nothing about
// the two ways forward. It is worth spelling them out: this failure is the
// client refusing the server, not the server refusing the client, so the fix
// is on this side.
func ExplainTLSError(err error, mode, host string) error {
	if err == nil || !tlsModeVerifies(mode) {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, "certificate") && !strings.Contains(msg, "x509") {
		return err
	}
	return fmt.Errorf("%w\n"+
		"  tlsMode=%s could not verify the certificate of %q.\n"+
		"  The CA that issued it is not in the system trust store.\n"+
		"    - point tlsCAFile or tlsCAPem at that CA\n"+
		"    - or use tlsMode=%s, which encrypts without verifying the server",
		err, mode, host, TLSModeRequire)
}
