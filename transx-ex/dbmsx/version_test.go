package dbmsx

import (
	"context"
	"strings"
	"testing"
)

// The checks below reach a decision before any connection is attempted, which is
// what makes them testable without a server. Everything past that point needs a
// live endpoint and is covered by the integration tests.

func TestCheckVersion_RejectsCrossEngine(t *testing.T) {
	_, err := CheckVersion(directMySQL(), sshPostgreSQL())
	if err == nil {
		t.Fatal("comparing versions across engines should be refused")
	}
	if !strings.Contains(err.Error(), "different database engines") {
		t.Errorf("error should name the reason, got: %v", err)
	}
}

func TestCheckVersion_ValidatesBothLocations(t *testing.T) {
	bad := directMySQL()
	bad.AccessType = ""

	if _, err := CheckVersion(bad, directMySQL()); err == nil {
		t.Error("an invalid source should be refused")
	} else if !strings.Contains(err.Error(), "source") {
		t.Errorf("error should name the offending side, got: %v", err)
	}

	if _, err := CheckVersion(directMySQL(), bad); err == nil {
		t.Error("an invalid destination should be refused")
	} else if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error should name the offending side, got: %v", err)
	}
}

// ServerVersion is a server-level call: it validates everything a connection
// needs but not loc.Database, which need not exist on a fresh target.
func TestServerVersion_DoesNotRequireDatabase(t *testing.T) {
	loc := directMySQL()
	loc.Database = ""
	loc.Direct.Host = ""

	_, err := ServerVersion(loc)
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("validation should fail on the missing host, not the database, got: %v", err)
	}
}

func TestEnforceVersion_SkipFlag(t *testing.T) {
	// Both endpoints point nowhere, so any attempt to connect would fail. The
	// flag has to short-circuit before that.
	model := DBMSMigrationModel{
		Source:           directMySQL(),
		Destination:      directMySQL(),
		SkipVersionCheck: true,
	}
	if err := enforceVersion(context.Background(), model); err != nil {
		t.Errorf("SkipVersionCheck should skip the check entirely, got: %v", err)
	}
}
