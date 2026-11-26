// bastion is the containers' SSH jump host (see package bastion): ssh -J <it> <user>@<container>.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"

	"github.com/syeyoung-cloud/containerservice/internal/bastion"
	"github.com/syeyoung-cloud/containerservice/internal/containers"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	listen := flag.String("listen", ":2222", "listen address")
	hostKeyFile := flag.String("host-key-file", "", "the bastion's SSH host key (OpenSSH private key) (required)")
	namespace := flag.String("namespace", "", "the containers' namespace (required)")
	public := flag.String("public", "", "the bastion's address as the owner reaches it, host:port, for messages")
	maxConns := flag.Int("max-conns", 64, "connections at once")
	flag.Parse()
	if *hostKeyFile == "" || *namespace == "" {
		log.Error("-host-key-file and -namespace are required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*hostKeyFile)
	if err != nil {
		log.Error("host key", "err", err)
		os.Exit(1)
	}
	key, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		log.Error("host key", "err", err)
		os.Exit(1)
	}
	srv := bastion.New(bastion.Config{
		HostKey: key,
		Target: func(name string) string {
			return fmt.Sprintf("%s-0.%s.%s.svc:22", name, containers.ServiceName, *namespace)
		},
		MaxConns: *maxConns,
		Public:   *public,
		Log:      log,
	})
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() { <-ctx.Done(); l.Close() }()
	log.Info("bastion listening", "addr", *listen, "version", version, "namespace", *namespace, "hostKey", ssh.FingerprintSHA256(key.PublicKey()))
	if err := srv.Serve(l); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
