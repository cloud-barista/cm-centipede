package base

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Test certificate authority
// =============================================================================

// testCA is a self-signed root plus the material to issue leaves from it, so
// the verify-ca callback can be exercised against a chain it should accept and
// one it should not.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, commonName string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return &testCA{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issue returns the DER of a leaf certificate for host, signed by this CA.
func (ca *testCA) issue(t *testing.T, host string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	return der
}

// =============================================================================
// Mode resolution
// =============================================================================

func TestTLSModeOfDefaultsToDisable(t *testing.T) {
	if got := TLSModeOf(nil); got != TLSModeDisable {
		t.Errorf("TLSModeOf(nil) = %q, want %q", got, TLSModeDisable)
	}
	if got := TLSModeOf(&DirectConfig{}); got != TLSModeDisable {
		t.Errorf("TLSModeOf(empty) = %q, want %q", got, TLSModeDisable)
	}
}

func TestValidateTLS(t *testing.T) {
	ca := newTestCA(t, "test-root")

	tests := []struct {
		name     string
		dbmsType string
		cfg      DirectConfig
		wantErr  string // substring; empty means the config is accepted
	}{
		{
			name: "empty is disable",
			cfg:  DirectConfig{Host: "db"},
		},
		{
			name: "every mode is accepted",
			cfg:  DirectConfig{Host: "db", TLSMode: TLSModeVerifyFull},
		},
		{
			name:    "unknown mode names the alternatives",
			cfg:     DirectConfig{Host: "db", TLSMode: "on"},
			wantErr: "use one of disable, prefer, require",
		},
		{
			name:    "two CA forms at once",
			cfg:     DirectConfig{Host: "db", TLSMode: TLSModeVerifyCA, TLSCAFile: "/x.pem", TLSCAPEM: string(ca.pem)},
			wantErr: "give one or neither",
		},
		{
			name:    "a CA on a mode that does not verify",
			cfg:     DirectConfig{Host: "db", TLSMode: TLSModeRequire, TLSCAPEM: string(ca.pem)},
			wantErr: "does not verify the server",
		},
		{
			name:     "MongoDB has no prefer",
			dbmsType: DBMSTypeMongoDB,
			cfg:      DirectConfig{Host: "db", TLSMode: TLSModePrefer},
			wantErr:  "not supported for MongoDB",
		},
		{
			name:     "MongoDB accepts require",
			dbmsType: DBMSTypeMongoDB,
			cfg:      DirectConfig{Host: "db", TLSMode: TLSModeRequire},
		},
		{
			name:    "a CA file that is not there",
			cfg:     DirectConfig{Host: "db", TLSMode: TLSModeVerifyCA, TLSCAFile: "/nonexistent/ca.pem"},
			wantErr: "read tlsCAFile",
		},
		{
			name:    "PEM text that holds no certificate",
			cfg:     DirectConfig{Host: "db", TLSMode: TLSModeVerifyCA, TLSCAPEM: "not a certificate"},
			wantErr: "contains no PEM certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTLS("source", tt.dbmsType, &tt.cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q, got none", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveTLSLoadsCAFromFile(t *testing.T) {
	ca := newTestCA(t, "test-root")
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	s, err := ResolveTLS(&DirectConfig{Host: "db", TLSMode: TLSModeVerifyFull, TLSCAFile: path})
	if err != nil {
		t.Fatalf("ResolveTLS: %v", err)
	}
	if !s.HasCustomRoots() {
		t.Error("HasCustomRoots() = false, want true")
	}
	if s.ServerName != "db" {
		t.Errorf("ServerName = %q, want %q — verify-full checks the hostname", s.ServerName, "db")
	}
}

func TestResolveTLSServerNameOnlyForVerifyFull(t *testing.T) {
	for _, mode := range []string{TLSModeDisable, TLSModePrefer, TLSModeRequire, TLSModeVerifyCA} {
		s, err := ResolveTLS(&DirectConfig{Host: "db", TLSMode: mode})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if s.ServerName != "" {
			t.Errorf("%s: ServerName = %q, want empty — only verify-full checks the hostname", mode, s.ServerName)
		}
	}
}

// =============================================================================
// Go TLS configuration
// =============================================================================

func TestGoTLSConfig(t *testing.T) {
	tests := []struct {
		mode        string
		wantNil     bool
		wantSkip    bool
		wantVerifyF bool // a VerifyPeerCertificate callback is installed
	}{
		{mode: TLSModeDisable, wantNil: true},
		{mode: TLSModePrefer, wantSkip: true},
		{mode: TLSModeRequire, wantSkip: true},
		{mode: TLSModeVerifyCA, wantSkip: true, wantVerifyF: true},
		{mode: TLSModeVerifyFull},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			s, err := ResolveTLS(&DirectConfig{Host: "db", TLSMode: tt.mode})
			if err != nil {
				t.Fatalf("ResolveTLS: %v", err)
			}
			cfg, err := s.GoTLSConfig()
			if err != nil {
				t.Fatalf("GoTLSConfig: %v", err)
			}
			if tt.wantNil {
				if cfg != nil {
					t.Fatal("expected no TLS config for disable")
				}
				return
			}
			if cfg == nil {
				t.Fatal("expected a TLS config")
			}
			if cfg.InsecureSkipVerify != tt.wantSkip {
				t.Errorf("InsecureSkipVerify = %v, want %v", cfg.InsecureSkipVerify, tt.wantSkip)
			}
			if (cfg.VerifyPeerCertificate != nil) != tt.wantVerifyF {
				t.Errorf("VerifyPeerCertificate present = %v, want %v",
					cfg.VerifyPeerCertificate != nil, tt.wantVerifyF)
			}
		})
	}
}

// TestVerifyCAChecksChainNotHostname is the reason verify-ca exists: a
// certificate signed by the configured CA is accepted even though it names a
// different host, while one signed by another CA is refused.
func TestVerifyCAChecksChainNotHostname(t *testing.T) {
	ours := newTestCA(t, "our-root")
	theirs := newTestCA(t, "other-root")

	s, err := ResolveTLS(&DirectConfig{
		Host: "connect-name", TLSMode: TLSModeVerifyCA, TLSCAPEM: string(ours.pem),
	})
	if err != nil {
		t.Fatalf("ResolveTLS: %v", err)
	}
	cfg, err := s.GoTLSConfig()
	if err != nil {
		t.Fatalf("GoTLSConfig: %v", err)
	}

	t.Run("wrong hostname still passes", func(t *testing.T) {
		leaf := ours.issue(t, "some-other-name")
		if err := cfg.VerifyPeerCertificate([][]byte{leaf}, nil); err != nil {
			t.Errorf("verify-ca rejected a certificate from the configured CA: %v", err)
		}
	})

	t.Run("wrong CA fails", func(t *testing.T) {
		leaf := theirs.issue(t, "connect-name")
		if err := cfg.VerifyPeerCertificate([][]byte{leaf}, nil); err == nil {
			t.Error("verify-ca accepted a certificate from an unknown CA")
		}
	})

	t.Run("no certificate fails", func(t *testing.T) {
		if err := cfg.VerifyPeerCertificate(nil, nil); err == nil {
			t.Error("verify-ca accepted an empty chain")
		}
	})
}

// =============================================================================
// PostgreSQL parameters
// =============================================================================

func TestPgParamsModeNames(t *testing.T) {
	// The mode names are libpq's own and pass through unchanged.
	for _, mode := range TLSModes {
		s, err := ResolveTLS(&DirectConfig{Host: "db", TLSMode: mode})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		sslmode, rootcert, cleanup, err := s.PgParams()
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		cleanup()
		if sslmode != mode {
			t.Errorf("sslmode = %q, want %q", sslmode, mode)
		}
		if rootcert != "" {
			t.Errorf("%s: sslrootcert = %q, want empty when no CA was given", mode, rootcert)
		}
	}
}

func TestPgParamsWritesPEMToATemporaryFile(t *testing.T) {
	ca := newTestCA(t, "test-root")
	s, err := ResolveTLS(&DirectConfig{Host: "db", TLSMode: TLSModeVerifyFull, TLSCAPEM: string(ca.pem)})
	if err != nil {
		t.Fatalf("ResolveTLS: %v", err)
	}

	_, rootcert, cleanup, err := s.PgParams()
	if err != nil {
		t.Fatalf("PgParams: %v", err)
	}
	if rootcert == "" {
		t.Fatal("sslrootcert is empty; libpq takes a path, so the PEM must be written out")
	}
	got, err := os.ReadFile(rootcert)
	if err != nil {
		t.Fatalf("read the temporary CA file: %v", err)
	}
	if string(got) != string(ca.pem) {
		t.Error("the temporary CA file does not hold the PEM that was given")
	}
	info, err := os.Stat(rootcert)
	if err != nil {
		t.Fatalf("stat the temporary CA file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("temporary CA file mode = %o, want 600", mode)
	}

	cleanup()
	if _, err := os.Stat(rootcert); !os.IsNotExist(err) {
		t.Error("cleanup did not remove the temporary CA file")
	}
}

func TestPgParamsUsesTheFilePathAsGiven(t *testing.T) {
	ca := newTestCA(t, "test-root")
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	s, err := ResolveTLS(&DirectConfig{Host: "db", TLSMode: TLSModeVerifyCA, TLSCAFile: path})
	if err != nil {
		t.Fatalf("ResolveTLS: %v", err)
	}
	_, rootcert, cleanup, err := s.PgParams()
	if err != nil {
		t.Fatalf("PgParams: %v", err)
	}
	defer cleanup()

	if rootcert != path {
		t.Errorf("sslrootcert = %q, want the path as given (%q)", rootcert, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the caller's CA file was disturbed: %v", err)
	}
}

// =============================================================================
// Registered-configuration naming
// =============================================================================

func TestTLSConfigName(t *testing.T) {
	ours := newTestCA(t, "our-root")
	theirs := newTestCA(t, "other-root")

	resolve := func(t *testing.T, mode, host, caPEM string) *TLSSettings {
		t.Helper()
		s, err := ResolveTLS(&DirectConfig{Host: host, TLSMode: mode, TLSCAPEM: caPEM})
		if err != nil {
			t.Fatalf("ResolveTLS: %v", err)
		}
		return s
	}

	base1 := resolve(t, TLSModeVerifyFull, "db", string(ours.pem))
	same := resolve(t, TLSModeVerifyFull, "db", string(ours.pem))
	otherCA := resolve(t, TLSModeVerifyFull, "db", string(theirs.pem))
	otherHost := resolve(t, TLSModeVerifyFull, "other", string(ours.pem))
	otherMode := resolve(t, TLSModeVerifyCA, "db", string(ours.pem))

	if TLSConfigName(base1) != TLSConfigName(same) {
		t.Error("the same configuration produced two names; re-registration would grow the map")
	}
	for _, other := range []struct {
		name string
		s    *TLSSettings
	}{
		{"a different CA", otherCA},
		{"a different host", otherHost},
		{"a different mode", otherMode},
	} {
		if TLSConfigName(base1) == TLSConfigName(other.s) {
			t.Errorf("%s produced the same name; one configuration would overwrite the other", other.name)
		}
	}

	if !strings.HasPrefix(TLSConfigName(base1), "dbmsx-") {
		t.Errorf("name %q does not carry the dbmsx- prefix", TLSConfigName(base1))
	}
}

// =============================================================================
// Diagnostics
// =============================================================================

func TestExplainTLSError(t *testing.T) {
	certErr := &x509.UnknownAuthorityError{}

	t.Run("adds guidance for a verifying mode", func(t *testing.T) {
		err := ExplainTLSError(certErr, TLSModeVerifyFull, "db.example.com")
		msg := err.Error()
		for _, want := range []string{"db.example.com", "tlsCAFile", "tlsMode=require"} {
			if !strings.Contains(msg, want) {
				t.Errorf("guidance does not mention %q:\n%s", want, msg)
			}
		}
	})

	t.Run("leaves other modes alone", func(t *testing.T) {
		if got := ExplainTLSError(certErr, TLSModeRequire, "db"); got != error(certErr) {
			t.Error("require does not verify, so there is nothing to explain")
		}
	})

	t.Run("leaves unrelated errors alone", func(t *testing.T) {
		other := os.ErrDeadlineExceeded
		if got := ExplainTLSError(other, TLSModeVerifyFull, "db"); got != other {
			t.Error("an error unrelated to certificates was rewritten")
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if got := ExplainTLSError(nil, TLSModeVerifyFull, "db"); got != nil {
			t.Error("nil was turned into an error")
		}
	})
}
