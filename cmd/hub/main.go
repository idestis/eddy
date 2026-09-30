// Command eddy-hub runs the Eddy hub: the web UI, HTTP API and MCP endpoint,
// and the endpoint that agents in workload clusters dial into.
//
// Usage:
//
//	eddy-hub [--config /etc/eddy/hub.yaml]
//	eddy-hub --version
//	eddy-hub hash-password                      # reads a password from stdin
//	eddy-hub admin revoke --user <subject> [--config …]
//	eddy-hub dev-config --contexts a,b --out .dev/hub.yaml   # -tags dev only
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/hub"
	"github.com/idestis/eddy/internal/version"
)

const defaultConfig = "/etc/eddy/hub.yaml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "hash-password":
			return hashPassword(args[1:], stdin, stdout, stderr)
		case "admin":
			return admin(args[1:], stdout, stderr)
		case "dev-config":
			return devConfig(args[1:], os.Getenv, stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("eddy-hub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "path to hub.yaml")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "eddy-hub: unknown command %q\n", fs.Arg(0))
		return 2
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(log)
	cfg, err := config.LoadHub(*cfgPath)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := hub.New(ctx, cfg, hub.Options{Log: log})
	if err != nil {
		log.Error("hub failed to start", "err", err)
		return 1
	}
	if err := h.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("hub stopped with an error", "err", err)
		return 1
	}
	log.Info("hub stopped")
	return 0
}

// logLevel reads EDDY_LOG_LEVEL (debug, info, warn, error; default info).
func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(os.Getenv("EDDY_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}
