// Package bastion is the containers' SSH jump host: an open hop that forwards to a container's sshd
// and does nothing else. It takes anyone (it has no keys to check: the container's sshd checks the
// owner's key, end to end through the forward) and allows one thing, a direct-tcpip forward to
// <container>:22, which it connects to as the container pod's DNS name. No shells, no commands, no
// other forwards.
package bastion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cyoung06/containerservice/internal/containers"
)

// Config is the bastion's.
type Config struct {
	HostKey ssh.Signer
	// Target is the address of a container's sshd, by container name.
	Target func(name string) string
	// Dial connects to a target (net.Dialer's by default).
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// MaxConns bounds connections at once; more are closed on accept.
	MaxConns int
	// MaxForwards bounds open forwards per connection.
	MaxForwards int
	// Handshake bounds the SSH handshake (and a forward's connect).
	Handshake time.Duration
	// Public is how the owner reaches the bastion (host:port), for the refusal of a shell.
	Public string
	Log    *slog.Logger
}

// Server is a running bastion.
type Server struct {
	cfg   Config
	ssh   *ssh.ServerConfig
	slots chan struct{}
	wg    sync.WaitGroup
}

// New makes a bastion. Zero limits take defaults (64 connections, 8 forwards each, 10 s).
func New(cfg Config) *Server {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 64
	}
	if cfg.MaxForwards <= 0 {
		cfg.MaxForwards = 8
	}
	if cfg.Handshake <= 0 {
		cfg.Handshake = 10 * time.Second
	}
	if cfg.Dial == nil {
		cfg.Dial = (&net.Dialer{}).DialContext
	}
	sc := &ssh.ServerConfig{NoClientAuth: true, ServerVersion: "SSH-2.0-containerservice-bastion"}
	sc.AddHostKey(cfg.HostKey)
	return &Server{cfg: cfg, ssh: sc, slots: make(chan struct{}, cfg.MaxConns)}
}

// Serve accepts on l until it's closed, then waits for the connections to end.
func (s *Server) Serve(l net.Listener) error {
	defer s.wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case s.slots <- struct{}{}:
		default:
			s.cfg.Log.Warn("too many connections", "from", c.RemoteAddr().String())
			c.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			s.conn(c)
		}()
	}
}

func (s *Server) conn(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(s.cfg.Handshake))
	sc, chans, reqs, err := ssh.NewServerConn(c, s.ssh)
	if err != nil {
		return // scanners and the like
	}
	defer sc.Close()
	c.SetDeadline(time.Time{})
	from := c.RemoteAddr().String()
	go ssh.DiscardRequests(reqs) // no global requests (tcpip-forward and the like): all refused
	forwards := make(chan struct{}, s.cfg.MaxForwards)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.Prohibited, fmt.Sprintf("This is a jump host only: ssh -J %s <user>@<container>", s.cfg.Public))
			continue
		}
		name, port, err := parseDirect(nc.ExtraData())
		if err != nil || port != 22 || !containers.ValidName(name) {
			nc.Reject(ssh.Prohibited, "Only <container>:22 can be reached through this host.")
			continue
		}
		select {
		case forwards <- struct{}{}:
		default:
			nc.Reject(ssh.ResourceShortage, "Too many forwards on this connection.")
			continue
		}
		go func() {
			defer func() { <-forwards }()
			s.forward(nc, name, from)
		}()
	}
}

// parseDirect reads a direct-tcpip open's target (RFC 4254 7.2).
func parseDirect(data []byte) (string, uint32, error) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(data, &p); err != nil {
		return "", 0, err
	}
	return p.Host, p.Port, nil
}

func (s *Server) forward(nc ssh.NewChannel, name, from string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Handshake)
	up, err := s.cfg.Dial(ctx, "tcp", s.cfg.Target(name))
	cancel()
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, fmt.Sprintf("The container %q isn't reachable (no such container, or it isn't running).", name))
		return
	}
	defer up.Close()
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	s.cfg.Log.Info("forward", "from", from, "container", name)
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, ch); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(ch, up); ch.CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
