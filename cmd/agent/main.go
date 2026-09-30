// Command agent runs the Eddy agent in a workload cluster. It is configured
// from EDDY_* environment variables (see internal/config.Agent) and uses the
// in-cluster ServiceAccount, falling back to KUBECONFIG (or ~/.kube/config)
// for local development.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/eddy-gitops/eddy/internal/agent"
	"github.com/eddy-gitops/eddy/internal/config"
	"github.com/eddy-gitops/eddy/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("agent: exiting", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadAgent()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	rc, err := restConfig()
	if err != nil {
		return err
	}
	rc.UserAgent = "eddy-agent/" + version.Version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Run(ctx, cfg, rc, logger)
}

func restConfig() (*rest.Config, error) {
	rc, err := rest.InClusterConfig()
	if err == nil {
		return rc, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rc, kerr := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if kerr != nil {
		return nil, fmt.Errorf("kubernetes config: not in a cluster (%v) and no kubeconfig: %w", err, kerr)
	}
	return rc, nil
}
