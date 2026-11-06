package base

// RedactedPassword stands in for the database password in a command built to be
// shown — logged, or kept in OperationError.Command. It is letters only, so the
// shell quoting and URL encoding the drivers apply leave it readable.
const RedactedPassword = "REDACTED"

// RedactedLoc returns loc with its database password replaced by
// RedactedPassword, for building a command that can be shown: the driver then
// quotes and encodes the stand-in exactly where it would put the password. The
// tunnel config is copied, so loc itself is left alone.
func RedactedLoc(loc DBMSLocation) DBMSLocation {
	if loc.SSHTunnel != nil && loc.SSHTunnel.Password != "" {
		cfg := *loc.SSHTunnel
		cfg.Password = RedactedPassword
		loc.SSHTunnel = &cfg
	}
	return loc
}
