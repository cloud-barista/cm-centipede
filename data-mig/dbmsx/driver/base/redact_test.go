package base

import "testing"

func TestRedactedLoc(t *testing.T) {
	loc := DBMSLocation{SSHTunnel: &SSHTunnelConfig{Username: "u", Password: "secret", SSH: &SSHConfig{Password: "sshpw"}}}
	got := RedactedLoc(loc)
	if got.SSHTunnel.Password != RedactedPassword || got.SSHTunnel.Username != "u" {
		t.Errorf("RedactedLoc tunnel = %+v", got.SSHTunnel)
	}
	if loc.SSHTunnel.Password != "secret" {
		t.Error("RedactedLoc changed the location it was given")
	}
	if got.SSHTunnel.SSH.Password != "sshpw" {
		t.Error("the SSH password is not in any command and is left as it is")
	}
	if none := RedactedLoc(DBMSLocation{}); none.SSHTunnel != nil {
		t.Error("a location without a tunnel stays without one")
	}
	if empty := RedactedLoc(DBMSLocation{SSHTunnel: &SSHTunnelConfig{}}); empty.SSHTunnel.Password != "" {
		t.Error("an empty password stays empty, so the command keeps its shape")
	}
}
