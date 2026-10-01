package core

import (
	"context"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// =============================================================================
// SSHDialer — TCP connections opened from an SSH host
// =============================================================================

// SSHDialer opens TCP connections from the SSH host described by its config
// rather than from this process: each DialContext asks the SSH server to connect
// to addr (a direct-tcpip channel) and hands back that channel as a net.Conn.
//
// It is what lets an HTTP client reach an endpoint that only the SSH host can
// see — set it as an http.Transport's DialContext and the requests travel inside
// the SSH connection unchanged. addr is resolved on the SSH host's side, so
// "127.0.0.1:9000" names a port on that host, not on this one.
//
// No local port is opened, so nothing has to be allocated or released per
// transfer, and TLS still verifies against the endpoint's own host name.
//
// The SSH connection is dialed on first use and shared by every channel opened
// through it. If it has gone away — an idle timeout on the host, for instance —
// the next DialContext reconnects once before giving up. Channels already open
// on the lost connection fail with it; only new ones are recovered.
//
// Instances are safe for concurrent use. The caller must Close the dialer.
type SSHDialer struct {
	cfg *SSHConfig

	mu     sync.Mutex
	client *ssh.Client
	closed bool
}

// NewSSHDialer returns a dialer that opens connections from the host cfg
// describes. It does not connect until the first DialContext.
func NewSSHDialer(cfg *SSHConfig) *SSHDialer {
	return &SSHDialer{cfg: cfg}
}

// DialContext opens a connection to addr from the SSH host. network must be a
// TCP network; the SSH protocol forwards nothing else.
func (d *SSHDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("ssh tunnel: unsupported network %q", network)
	}

	client, err := d.connected()
	if err != nil {
		return nil, err
	}

	conn, err := client.DialContext(ctx, network, addr)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ssh tunnel dial %s: %w", addr, err)
	}

	// The error does not say whether the SSH connection died or the host could
	// not reach addr, so the connection is replaced and the dial tried once more.
	// A second failure is the host's answer and is returned as such.
	client, reconnErr := d.reconnect(client)
	if reconnErr != nil {
		return nil, fmt.Errorf("ssh tunnel dial %s (%v) and reconnect failed: %w", addr, err, reconnErr)
	}
	conn, err = client.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("ssh tunnel dial %s: %w", addr, err)
	}
	return conn, nil
}

// Close releases the SSH connection, if any. It is safe to call more than once.
func (d *SSHDialer) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	d.closed = true
	if d.client == nil {
		return nil
	}
	err := d.client.Close()
	d.client = nil
	return err
}

// connected returns the shared SSH connection, dialing it on first use.
func (d *SSHDialer) connected() (*ssh.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil, fmt.Errorf("ssh tunnel: already closed")
	}
	if d.client == nil {
		client, err := dial(d.cfg)
		if err != nil {
			return nil, err
		}
		d.client = client
	}
	return d.client, nil
}

// reconnect replaces stale with a fresh connection. When another caller has
// already replaced it, that replacement is returned instead of dialing again.
func (d *SSHDialer) reconnect(stale *ssh.Client) (*ssh.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil, fmt.Errorf("ssh tunnel: already closed")
	}
	if d.client != nil && d.client != stale {
		return d.client, nil
	}
	if d.client != nil {
		d.client.Close()
		d.client = nil
	}

	client, err := dial(d.cfg)
	if err != nil {
		return nil, err
	}
	d.client = client
	return client, nil
}
