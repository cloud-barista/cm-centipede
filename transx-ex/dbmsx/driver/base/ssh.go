package base

import "github.com/cloud-barista/cm-centipede/transx-ex/core"

// SSH access is implemented once in core and shared with the storage domain.
// This file re-exports it so driver code keeps using unqualified base names.

// SSHConfig holds SSH server connection parameters. See core.SSHConfig.
type SSHConfig = core.SSHConfig

// SSHExec runs commands on a remote host, reusing one connection. See core.SSHExec.
type SSHExec = core.SSHExec

// SSHRunError reports a remote command that exited non-zero. See core.SSHRunError.
type SSHRunError = core.SSHRunError

var (
	// ExecuteViaSSH dials, runs one command, and closes. See core.Exec.
	ExecuteViaSSH = core.Exec

	// NewSSHExec returns an executor that dials per Run. See core.NewSSHExec.
	NewSSHExec = core.NewSSHExec

	// DialSSHExec opens a connection reused across Runs. See core.DialSSHExec.
	DialSSHExec = core.DialSSHExec

	// SSHStderr extracts the stderr captured for a failed remote command.
	SSHStderr = core.SSHStderr
)
