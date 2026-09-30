// Package agent is the Eddy agent that runs in every workload cluster.
//
// It watches Flux objects and workloads with dynamic informers, keeps a
// summary of each (model.Resource), and streams those summaries to the hub
// over a WebSocket that the agent dials out. Requests from the hub (reconcile,
// suspend, resume, yaml, events, logs) run through a client impersonating the
// requesting user, after the agent re-validates that identity; access checks
// use SubjectAccessReviews created by the agent's own ServiceAccount.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/version"
)

// Impersonated clients are cached per identity.
const (
	clientCacheSize = 64
	clientCacheTTL  = 5 * time.Minute
)

// Run starts the agent and blocks until ctx is done. rc is the agent's own
// (ServiceAccount) configuration; it is used unmodified only for discovery,
// informers and SubjectAccessReviews.
func Run(ctx context.Context, cfg *config.Agent, rc *rest.Config, logger *slog.Logger) error {
	return run(ctx, cfg, rc, logger, nil)
}

// configureFunc adjusts the handler and Hello before the session starts. Only
// local mode (dev builds) passes one; kube and dyn are the clients built from rc.
type configureFunc func(h *Handler, hello *protocol.Hello, kube kubernetes.Interface, dyn dynamic.Interface)

func run(ctx context.Context, cfg *config.Agent, rc *rest.Config, logger *slog.Logger, configure configureFunc) error {
	kube, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("agent: build client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("agent: build dynamic client: %w", err)
	}
	tlsCfg, err := hubTLS(cfg.CAFile)
	if err != nil {
		return err
	}

	served, err := flux.Discover(kube.Discovery())
	if err != nil {
		return err
	}
	if _, ok := served[flux.KindKustomization]; !ok {
		logger.Warn("agent: Flux kinds are not served; watching workloads only")
	}
	info, err := kube.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("agent: read server version: %w", err)
	}
	fluxVersion := flux.DetectFluxVersion(ctx, kube, flux.FluxNamespace)
	logger.Info("agent: starting", "version", version.Version, "cluster", cfg.Cluster,
		"kubernetes", info.GitVersion, "flux", fluxVersion, "kinds", served, "namespaces", cfg.Namespaces)

	c, err := NewCache(dyn, served, cfg.Namespaces, logger)
	if err != nil {
		return err
	}
	h := &Handler{
		Policy:      Policy{AllowedGroupPrefixes: cfg.AllowedGroupPrefixes, AllowedGroups: cfg.AllowedGroups, DenyUserPrefixes: cfg.DenyUserPrefixes},
		Served:      served,
		Impersonate: ImpersonatingFactory(rc, clientCacheSize, clientCacheTTL),
		Self:        kube,
		Logger:      logger,
	}
	sess := &Session{
		URL:     cfg.HubURL,
		Cluster: cfg.Cluster,
		Token:   cfg.Token,
		TLS:     tlsCfg,
		Hello: protocol.Hello{
			AgentVersion:      version.Version,
			KubernetesVersion: info.GitVersion,
			FluxVersion:       fluxVersion,
			Namespaces:        cfg.Namespaces,
		},
		Source:  c,
		Handler: h,
		Logger:  logger,
	}
	if configure != nil {
		configure(h, &sess.Hello, kube, dyn)
	}

	// A health server failure (for example a port in use) stops the agent.
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		if cfg.HealthAddr == "" {
			return // local mode runs several sessions in one process without one
		}
		if err := serveHealth(runCtx, cfg.HealthAddr, c.Synced, sess.Connected); err != nil {
			cancel(err)
		}
	}()

	c.Start(runCtx)
	if c.WaitForSync(runCtx) {
		logger.Info("agent: informers synced")
		if err := sess.Run(runCtx); err != nil {
			cancel(err)
		}
	}
	cancel(nil)
	<-healthDone
	if err := context.Cause(runCtx); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// hubTLS trusts the system roots plus, when set, the CA bundle in caFile.
func hubTLS(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("agent: read hub CA: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("agent: no certificates in hub CA file %s", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
