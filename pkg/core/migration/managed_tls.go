package migration

import (
	"fmt"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
	"github.com/rs/zerolog/log"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	"github.com/cloud-barista/cm-centipede/pkg/client/beetle"
)

// tlsModeAuto is the BeetleDBRef.TLSMode that leaves the choice to cm-beetle.
// It never reaches transx-ex, which knows only the five modes it enforces:
// resolveManagedTLS turns it into one of them first.
const tlsModeAuto = "auto"

// Values of beetle.SecureTransport.RecommendedSSLMode, as cb-spider names them.
const (
	sslModeDisabled       = "DISABLED"
	sslModeVerifyCA       = "VERIFY_CA"
	sslModeVerifyIdentity = "VERIFY_IDENTITY"
)

// caExpiryWarning is how far ahead of its expiry a collected CA is warned about.
const caExpiryWarning = 30 * 24 * time.Hour

// tlsDecision is the transport security a managed connection ends up with.
type tlsDecision struct {
	Mode  string
	CAPEM string

	// Reason names what the mode was decided from; Warns lists every way the
	// decision is weaker than, or differs from, what the caller might expect.
	// Both are empty for an explicit mode, which is not decided but given.
	Reason string
	Warns  []string
}

// resolveManagedTLS returns the TLS mode and CA a managed (beetleDb) connection
// uses.
//
// An explicit mode is the caller's, and is passed through with its CA exactly as
// given: what the pair means, and whether it is consistent, is for transx-ex to
// judge, as on an inline db connection. An empty mode or "auto" is decided from
// what cm-beetle reports on the instance (decideAutoTLS), asked again on every
// connection so a rotated certificate is picked up, and logged.
func resolveManagedTLS(ref commonmodel.BeetleDBRef, acc *beetle.DBConfig) (tlsDecision, error) {
	if ref.TLSMode != "" && ref.TLSMode != tlsModeAuto {
		return tlsDecision{Mode: ref.TLSMode, CAPEM: ref.TLSCAPEM}, nil
	}

	st, lookupErr := beetle.GetSecureTransport(ref, pick(ref.Username, acc.Username))
	d, err := decideAutoTLS(st, lookupErr, ref.TLSCAPEM != "", time.Now())
	if err != nil {
		return tlsDecision{}, fmt.Errorf("beetle rdbms %q: %w", ref.RdbmsID, err)
	}

	ev := log.Info()
	if len(d.Warns) > 0 {
		ev = log.Warn().Strs("warnings", d.Warns)
	}
	ev.Str("rdbmsId", ref.RdbmsID).
		Str("requested", ref.TLSMode).
		Str("tlsMode", d.Mode).
		Str("reason", d.Reason).
		Msg("beetleDb: resolved tlsMode auto")
	return d, nil
}

// decideAutoTLS picks a TLS mode from what cm-beetle reported on the instance.
// st is nil when the lookup failed, and lookupErr then says why.
//
// The decision rests on cm-beetle's report alone. userCAGiven only adds a
// warning that the caller's CA goes unused: a caller who wants their own CA
// checked names the verify mode that checks it.
//
// The CA is cleared wherever the mode does not verify, since transx-ex refuses
// a CA it would not consult. now is the clock the collected CA's expiry is
// judged against.
func decideAutoTLS(st *beetle.SecureTransport, lookupErr error, userCAGiven bool, now time.Time) (tlsDecision, error) {
	var d tlsDecision

	switch {
	case st == nil:
		// Nothing to go on: encrypt if the server offers it, as cb-spider's own
		// probe does, rather than fail a migration over a lookup.
		d.Mode = transxex.TLSModePrefer
		d.Reason = "cm-beetle secure-transport lookup failed"
		d.Warns = append(d.Warns, fmt.Sprintf(
			"secure-transport lookup failed, connecting with tlsMode %s: %v", d.Mode, lookupErr))

	case st.RecommendedSSLMode == sslModeVerifyIdentity, st.RecommendedSSLMode == sslModeVerifyCA:
		d.Reason = "cm-beetle recommendedSSLMode=" + st.RecommendedSSLMode
		if st.CAPEM == "" {
			// A managed instance's CA is rarely in the system trust store, so a
			// verify mode without one would fail every time. Keep the encryption.
			d.Mode = transxex.TLSModeRequire
			d.Warns = append(d.Warns, fmt.Sprintf(
				"%s recommends certificate verification but no CA was collected, "+
					"connecting with tlsMode %s (encrypted, unverified)", d.Reason, d.Mode))
			break
		}
		d.Mode = transxex.TLSModeVerifyCA
		if st.RecommendedSSLMode == sslModeVerifyIdentity {
			d.Mode = transxex.TLSModeVerifyFull
		}
		d.CAPEM = st.CAPEM
		if w := caExpiryWarn(st.CANotAfter, now); w != "" {
			d.Warns = append(d.Warns, w)
		}

	case st.RecommendedSSLMode == sslModeDisabled:
		if st.RequireSecureTransport {
			// The report contradicts itself; guessing either way could be wrong.
			return tlsDecision{}, fmt.Errorf(
				"cm-beetle reports recommendedSSLMode=%s yet the server requires secure transport; "+
					"set tlsMode explicitly", st.RecommendedSSLMode)
		}
		d.Mode = transxex.TLSModeDisable
		d.Reason = "cm-beetle recommendedSSLMode=" + st.RecommendedSSLMode

	case st.RecommendedSSLMode == "":
		// The server offers TLS, but its CA could not be collected.
		d.Mode = transxex.TLSModePrefer
		d.Reason = "cm-beetle gave no recommendedSSLMode"
		d.Warns = append(d.Warns, fmt.Sprintf(
			"no recommendedSSLMode (caCertificateError: %q), connecting with tlsMode %s",
			st.CAError, d.Mode))

	default:
		// A value this code does not know means the schema moved; do not let
		// that quietly fall back to a weaker mode.
		return tlsDecision{}, fmt.Errorf(
			"cm-beetle reports an unknown recommendedSSLMode %q; set tlsMode explicitly",
			st.RecommendedSSLMode)
	}

	if userCAGiven {
		d.Warns = append(d.Warns, "tlsCAPem is ignored under tlsMode auto; "+
			"name verify-ca or verify-full to verify against it")
	}
	return d, nil
}

// caExpiryWarn returns a warning when the collected CA has expired or is about
// to; empty when it is fine or its expiry is unknown.
func caExpiryWarn(notAfter, now time.Time) string {
	switch {
	case notAfter.IsZero():
		return ""
	case !now.Before(notAfter):
		return fmt.Sprintf("collected CA expired at %s", notAfter.Format(time.RFC3339))
	case notAfter.Sub(now) < caExpiryWarning:
		return fmt.Sprintf("collected CA expires at %s", notAfter.Format(time.RFC3339))
	default:
		return ""
	}
}
