//go:build dev

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/devlocal"
)

// devConfig writes the hub.yaml for local mode (task dev): one static
// cluster per kubeconfig context, sharing one generated agent token. It reads
// the kubeconfig file only; no cluster is contacted.
func devConfig(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eddy-hub dev-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contexts := fs.String("contexts", "", "comma-separated kubeconfig contexts, each optionally renamed with ctx=name (default: the current context)")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	protect := fs.String("protect", devlocal.DefaultProtect, "regex of contexts or cluster names marked protected (and kept read-only by the agent)")
	out := fs.String("out", ".dev/hub.yaml", "where to write the hub config")
	sqlite := fs.Bool("sqlite", false, "use the sqlite store at .dev/eddy.db instead of memory, to keep threads across restarts")
	tokenFile := fs.String("token-file", ".dev/agent-token", "shared agent token; generated when missing")
	users := fs.String("users", "hack/users.dev.yaml", "local users file")
	key := fs.String("key", "hack/dev.key", "auth key file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "eddy-hub dev-config: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "eddy-hub dev-config:", err)
		return 1
	}

	re, err := devlocal.CompileProtect(*protect)
	if err != nil {
		return fail(err)
	}
	kc, err := devlocal.LoadKubeconfig(*kubeconfig)
	if err != nil {
		return fail(err)
	}
	targets, err := devlocal.ParseTargets(*contexts, kc, re)
	if err != nil {
		return fail(err)
	}
	if _, err := devlocal.EnsureToken(*tokenFile); err != nil {
		return fail(err)
	}
	o := devlocal.HubOptions{Targets: targets, UsersFile: *users, KeyFile: *key}
	if *sqlite {
		o.SQLitePath = filepath.Join(filepath.Dir(*out), "eddy.db")
	}
	ai, err := devlocal.AIFromEnv(getenv)
	if err != nil {
		return fail(err)
	}
	o.AI = ai
	b := devlocal.RenderHub(o)
	if _, err := config.ParseHub(b); err != nil {
		return fail(fmt.Errorf("generated config is invalid: %w", err))
	}
	// An unchanged file is not rewritten, so a hub under air does not restart.
	if old, err := os.ReadFile(*out); err == nil && bytes.Equal(old, b) {
		fmt.Fprintf(stdout, "%s is up to date\n", *out)
	} else {
		if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(*out, b, 0o600); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "wrote %s\n", *out)
	}
	for _, t := range targets {
		state := "read-only unless the agent runs with --allow-writes"
		if t.Protected {
			state = "PROTECTED: typed confirmation on the hub, read-only unless --allow-writes-protected"
		}
		fmt.Fprintf(stdout, "  cluster %-24s context %s (%s)\n", t.Cluster, t.Context, state)
	}
	fmt.Fprintln(stdout, "  "+aiNote(ai))
	return 0
}

// aiNote describes the Ask AI choice in one line.
func aiNote(ai devlocal.AIOptions) string {
	switch ai.Provider {
	case "anthropic":
		return "Ask AI: Anthropic API (key from ANTHROPIC_API_KEY)"
	case "bedrock":
		return "Ask AI: Amazon Bedrock in " + ai.BedrockRegion + " (credentials from the AWS default chain, e.g. AWS_PROFILE)"
	}
	return "Ask AI: off (set EDDY_AI_PROVIDER=anthropic or bedrock to enable it; see docs/development.md)"
}
