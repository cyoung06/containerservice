// containerservice launches its owner's containers: StatefulSets of one pod in one namespace, from
// any image, with volumes, a web terminal and SSH (through its own bastion, later). One owner,
// signed in with OpenID Connect; the owner never needs Kubernetes credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/cyoung06/containerservice/internal/api"
	"github.com/cyoung06/containerservice/internal/auth"
	"github.com/cyoung06/containerservice/internal/containers"
	"github.com/cyoung06/containerservice/internal/store"
	"github.com/cyoung06/containerservice/internal/terminal"
	"github.com/cyoung06/containerservice/internal/web"
)

var version = "dev"

// publicSSH parses -ssh-public (host:port); empty: no bastion is advertised.
func publicSSH(public string) (*api.SSH, error) {
	if public == "" {
		return nil, nil
	}
	host, port, err := net.SplitHostPort(public)
	n, perr := strconv.Atoi(port)
	if err != nil || perr != nil || n < 1 || n > 65535 || host == "" {
		return nil, fmt.Errorf("need host:port, got %q", public)
	}
	return &api.SSH{Host: host, Port: n}, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var a auth.Config
	var cc containers.Config
	listen := flag.String("listen", ":8080", "listen address (plain HTTP; TLS terminates in front)")
	flag.StringVar(&cc.Namespace, "namespace", "", "the namespace of the containers and volumes (required)")
	flag.StringVar(&cc.PullSecret, "pull-secret", "", "an image pull secret in the namespace that every container's pod lists (empty: none)")
	databaseURLFile := flag.String("database-url-file", "", "file holding the Postgres URL, e.g. postgres://user:pass@host/db (required)")
	flag.StringVar(&a.Origin, "origin", "", "the public origin, e.g. https://containers.example.org (required)")
	flag.StringVar(&a.Issuer, "oidc-issuer", "", "the OpenID Connect issuer URL (required)")
	flag.StringVar(&a.ClientID, "oidc-client-id", "", "the OIDC client id (required)")
	clientSecretFile := flag.String("oidc-client-secret-file", "", "file holding the OIDC client secret (required)")
	flag.StringVar(&a.Group, "group", "", "the group the ID token must list to sign in: the owner (required)")
	flag.StringVar(&a.GroupsClaim, "groups-claim", "groups", "the ID token claim with the user's groups")
	flag.StringVar(&a.UsernameClaim, "username-claim", "preferred_username", "the ID token claim with the username")
	flag.StringVar(&a.KeyFile, "session-key-file", "", "file holding the 32-byte session cookie key, raw or base64 (required)")
	flag.DurationVar(&a.Lifetime, "session-lifetime", 7*24*time.Hour, "how long a sign-in lasts")
	var ui web.Config
	flag.StringVar(&ui.Title, "title", "Containers", "name shown in the sidebar and the tab title")
	flag.StringVar(&ui.LoginProvider, "login-provider", "", "the sign-in provider's name for the button, e.g. \"Keycloak\" (empty: \"Sign in\")")
	sshPublic := flag.String("ssh-public", "", "the bastion's address as the owner reaches it, host:port, shown in the UI (empty: none)")
	flag.Parse()

	for name, v := range map[string]string{"namespace": cc.Namespace, "database-url-file": *databaseURLFile, "origin": a.Origin, "oidc-issuer": a.Issuer,
		"oidc-client-id": a.ClientID, "oidc-client-secret-file": *clientSecretFile, "group": a.Group, "session-key-file": a.KeyFile} {
		if v == "" {
			log.Error("-" + name + " is required")
			os.Exit(2)
		}
	}
	a.Origin = strings.TrimRight(a.Origin, "/")
	sshAddr, err := publicSSH(*sshPublic)
	if err != nil {
		log.Error("-ssh-public", "err", err)
		os.Exit(2)
	}
	secret, err := os.ReadFile(*clientSecretFile)
	if err != nil {
		log.Error("client secret", "err", err)
		os.Exit(1)
	}
	a.ClientSecret = strings.TrimSpace(string(secret))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	dbURL, err := os.ReadFile(*databaseURLFile)
	if err != nil {
		log.Error("database url", "err", err)
		os.Exit(1)
	}
	db, err := store.Open(ctx, strings.TrimSpace(string(dbURL)))
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	signIn, err := auth.New(ctx, a, log)
	if err != nil {
		log.Error("sign-in", "err", err)
		os.Exit(1)
	}
	// In the cluster: the service account; elsewhere: $KUBECONFIG or ~/.kube/config.
	kube, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
	if err != nil {
		log.Error("kubernetes config", "err", err)
		os.Exit(1)
	}
	client, err := kubernetes.NewForConfig(kube)
	if err != nil {
		log.Error("kubernetes client", "err", err)
		os.Exit(1)
	}
	svc, err := containers.New(ctx, client, cc, db, log)
	if err != nil {
		log.Error("containers", "err", err)
		os.Exit(1)
	}
	// The Secret follows the database; a sync that failed after a key change is redone here.
	if err := svc.SyncKeys(ctx); err != nil {
		log.Error("syncing the SSH keys", "err", err)
		os.Exit(1)
	}
	term, err := terminal.New(kube, kube.Host)
	if err != nil {
		log.Error("terminal", "err", err)
		os.Exit(1)
	}

	handler, err := web.Handler(web.UI(), ui, a.Origin, signIn.Routes, api.New(signIn, svc, db, term, a.Origin, sshAddr, log).Routes)
	if err != nil {
		log.Error("ui", "err", err)
		os.Exit(1)
	}
	// The event stream and the terminals are long-lived: each clears these deadlines for itself.
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: time.Minute}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Info("containerservice listening", "addr", *listen, "version", version, "namespace", cc.Namespace)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
