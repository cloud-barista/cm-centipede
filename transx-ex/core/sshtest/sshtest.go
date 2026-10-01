// Package sshtest provides an in-process SSH server for exercising the
// SSH code paths of transx-ex without a bastion host.
//
// Its purpose is observability: the server counts completed handshakes and exec
// requests, which lets tests assert that a multi-query operation reuses one
// connection instead of dialing per query. No external SSH server can report
// that, and a per-query handshake is the kind of regression that stays invisible
// until it shows up as latency on a wide database.
//
// By default every exec'd command is echoed back on stdout; set Responder to
// return canned output for specific commands.
package sshtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Server is an SSH server listening on loopback that accepts any client.
type Server struct {
	// Responder, when set, produces the stdout for an exec'd command. The
	// default echoes the command back.
	Responder func(cmd string) string

	listener net.Listener
	hostKey  ssh.Signer

	mu          sync.Mutex
	connections int
	commands    []string
	conns       []*ssh.ServerConn

	wg sync.WaitGroup
}

// New starts a server on an ephemeral loopback port. The caller must Close it.
func New() (*Server, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("host signer: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &Server{listener: ln, hostKey: signer}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Close stops the listener and waits for in-flight connections to finish.
func (s *Server) Close() error {
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

// Host returns the address the server listens on.
func (s *Server) Host() string { return s.listener.Addr().(*net.TCPAddr).IP.String() }

// Port returns the port the server listens on.
func (s *Server) Port() int { return s.listener.Addr().(*net.TCPAddr).Port }

// Connections returns the number of completed SSH handshakes.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections
}

// Commands returns the commands exec'd so far, in order.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.commands))
	copy(out, s.commands)
	return out
}

// Execs returns the number of exec requests served.
func (s *Server) Execs() int { return len(s.Commands()) }

// DropConnections closes every established connection, simulating an idle
// timeout on a bastion.
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()

	for _, c := range conns {
		c.Close()
	}
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		nConn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(nConn)
		}()
	}
}

func (s *Server) handleConn(nConn net.Conn) {
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(s.hostKey)

	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		nConn.Close()
		return
	}
	defer sshConn.Close()

	s.mu.Lock()
	s.connections++
	s.conns = append(s.conns, sshConn)
	s.mu.Unlock()

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only sessions are supported") //nolint:errcheck
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}
		s.handleSession(ch, chReqs)
	}
}

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
			continue
		}

		cmd := parseExecPayload(req.Payload)
		if req.WantReply {
			req.Reply(true, nil) //nolint:errcheck
		}

		s.mu.Lock()
		s.commands = append(s.commands, cmd)
		s.mu.Unlock()

		// Drain stdin so a client writing input never blocks on the window.
		drained := make(chan struct{})
		go func() {
			io.Copy(io.Discard, ch) //nolint:errcheck
			close(drained)
		}()

		out := cmd
		if s.Responder != nil {
			out = s.Responder(cmd)
		}
		io.WriteString(ch, out) //nolint:errcheck
		ch.CloseWrite()         //nolint:errcheck
		<-drained

		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0})) //nolint:errcheck
		return
	}
}

// parseExecPayload extracts the command from an SSH "exec" request payload,
// which is a 4-byte big-endian length followed by the command bytes.
func parseExecPayload(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(payload[:4])
	if int(n) > len(payload)-4 {
		return ""
	}
	return string(payload[4 : 4+n])
}
