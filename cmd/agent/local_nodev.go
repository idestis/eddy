//go:build !dev

package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"strings"
)

// errLocalNeedsDev is returned for --local in a release build.
var errLocalNeedsDev = errors.New("local mode requires a dev build (go run -tags dev ./cmd/agent --local)")

// localFlags does not exist in release builds: --local is not a flag here.
type localFlags struct{}

func addLocalFlags(*flag.FlagSet, func(string) string) *localFlags { return nil }

func (*localFlags) enabled() bool                { return false }
func (*localFlags) validate(*flag.FlagSet) error { return nil }

func runLocal(context.Context, *localFlags, func(string) string, *slog.Logger) error {
	return errLocalNeedsDev
}

// rejectLocal refuses --local and EDDY_AGENT_LOCAL in a release build
// instead of silently starting the normal, impersonating agent.
func rejectLocal(args []string, getenv func(string) string) error {
	if v := getenv("EDDY_AGENT_LOCAL"); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return errLocalNeedsDev
	}
	for _, a := range args {
		if a == "--" {
			return nil
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "local" {
			return errLocalNeedsDev
		}
	}
	return nil
}
