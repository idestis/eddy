// Command agent runs the Eddy agent in a workload cluster. It is configured
// from EDDY_* environment variables (see internal/config.Agent) and uses the
// in-cluster ServiceAccount, falling back to KUBECONFIG (or ~/.kube/config)
// for local development.
//
// Binaries built with -tags dev also have --local, which serves kubeconfig
// contexts from a developer machine with the kubeconfig's own identity (see
// local_dev.go and docs/development.md). Release binaries do not.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/idestis/eddy/internal/agent"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/version"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func runMain(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eddy-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	lf := addLocalFlags(fs, getenv)
	if err := rejectLocal(args, getenv); err != nil {
		fmt.Fprintln(stderr, "eddy-agent:", err)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "eddy-agent: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if err := lf.validate(fs); err != nil {
		fmt.Fprintln(stderr, "eddy-agent:", err)
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: logLevel(getenv)}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if lf.enabled() {
		err = runLocal(ctx, lf, getenv, logger)
	} else {
		err = run(ctx, logger)
	}
	if err != nil {
		logger.Error("agent: exiting", "error", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.LoadAgent()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	rc, err := restConfig()
	if err != nil {
		return err
	}
	rc.UserAgent = "eddy-agent/" + version.Version
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

// logLevel reads EDDY_LOG_LEVEL (debug, info, warn, error; default info).
func logLevel(getenv func(string) string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(getenv("EDDY_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}
