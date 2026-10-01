//go:build dev

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/idestis/eddy/internal/agent"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/devlocal"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/version"
)

// Environment equivalents of the local-mode flags. A flag given on the
// command line wins over its variable. --allow-writes-protected has no
// variable on purpose: it must be typed.
const (
	envLocal       = "EDDY_AGENT_LOCAL"
	envContexts    = "EDDY_AGENT_CONTEXTS"
	envAllowWrites = "EDDY_AGENT_ALLOW_WRITES"
	envProtect     = "EDDY_AGENT_PROTECT"
	// envPresets overrides the watch presets of local mode, which enables
	// every preset by default; "none" disables them.
	envPresets = "EDDY_AGENT_PRESETS"
)

// localPresets returns the watch presets of local mode: every preset unless
// EDDY_AGENT_PRESETS names some, or "none".
func localPresets(getenv func(string) string) ([]string, error) {
	v := strings.TrimSpace(getenv(envPresets))
	switch v {
	case "":
		return flux.Presets(), nil
	case "none":
		return nil, nil
	}
	return flux.ParsePresets(strings.Split(v, ","))
}

// localFlags are the --local flags of a dev build.
type localFlags struct {
	local                bool
	contexts             string
	kubeconfig           string
	hubURL               string
	tokenFile            string
	protect              string
	allowWrites          bool
	allowWritesProtected bool

	envErr error // an unparsable boolean variable
}

func addLocalFlags(fs *flag.FlagSet, getenv func(string) string) *localFlags {
	lf := &localFlags{}
	envBool := func(k string) bool {
		v := getenv(k)
		if v == "" {
			return false
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			lf.envErr = errors.Join(lf.envErr, fmt.Errorf("%s=%q is not a boolean", k, v))
		}
		return b
	}
	envOr := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	fs.BoolVar(&lf.local, "local", envBool(envLocal), "DEV: serve kubeconfig contexts from this machine with your own kubeconfig identity (needs EDDY_DEV_MODE=1 and a loopback hub) [$"+envLocal+"]")
	fs.StringVar(&lf.contexts, "contexts", getenv(envContexts), "DEV: comma-separated kubeconfig contexts, each optionally renamed with ctx=name (default: the current context) [$"+envContexts+"]")
	fs.StringVar(&lf.kubeconfig, "kubeconfig", "", "DEV: kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&lf.hubURL, "hub", envOr("EDDY_HUB_URL", devlocal.DefaultHubURL), "DEV: hub agent endpoint; must be a loopback address [$EDDY_HUB_URL]")
	fs.StringVar(&lf.tokenFile, "token-file", ".dev/agent-token", "DEV: agent token file, used when EDDY_AGENT_TOKEN and "+devlocal.TokenEnv+" are unset")
	fs.StringVar(&lf.protect, "protect", envOr(envProtect, devlocal.DefaultProtect), "DEV: regex of contexts or cluster names that stay read-only even with --allow-writes [$"+envProtect+"]")
	fs.BoolVar(&lf.allowWrites, "allow-writes", envBool(envAllowWrites), "DEV: allow reconcile, suspend and resume (as your kubeconfig identity) [$"+envAllowWrites+"]")
	fs.BoolVar(&lf.allowWritesProtected, "allow-writes-protected", false, "DEV: also allow writes on contexts matching --protect (needs --allow-writes)")
	return lf
}

func (lf *localFlags) enabled() bool { return lf.local }

// validate rejects local-mode flags given without --local (or
// EDDY_AGENT_LOCAL=1), so a typo cannot silently start the normal,
// impersonating agent.
func (lf *localFlags) validate(fs *flag.FlagSet) error {
	if lf.envErr != nil {
		return lf.envErr
	}
	if lf.local {
		return nil
	}
	var set []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "version" && f.Name != "local" {
			set = append(set, "--"+f.Name)
		}
	})
	if len(set) > 0 {
		return fmt.Errorf("%s only apply with --local", strings.Join(set, ", "))
	}
	return nil
}

func rejectLocal([]string, func(string) string) error { return nil }

// localPlan is what runLocal will start, resolved before anything connects.
type localPlan struct {
	hubURL     string
	token      string
	kubeconfig *clientcmdapi.Config
	targets    []devlocal.Target
}

// planLocal checks every activation condition and resolves the contexts. It
// reads the kubeconfig file but contacts no cluster.
func planLocal(lf *localFlags, getenv func(string) string) (*localPlan, error) {
	if err := devlocal.CheckActivation(getenv, lf.hubURL); err != nil {
		return nil, err
	}
	if lf.allowWritesProtected && !lf.allowWrites {
		return nil, errors.New("local mode: --allow-writes-protected needs --allow-writes")
	}
	token := getenv("EDDY_AGENT_TOKEN")
	if token == "" {
		token = getenv(devlocal.TokenEnv)
	}
	if token == "" {
		b, err := os.ReadFile(lf.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("local mode: no agent token (set EDDY_AGENT_TOKEN or run `task dev:config`): %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return nil, errors.New("local mode: the agent token is empty")
	}
	protect, err := devlocal.CompileProtect(lf.protect)
	if err != nil {
		return nil, err
	}
	kc, err := devlocal.LoadKubeconfig(lf.kubeconfig)
	if err != nil {
		return nil, err
	}
	targets, err := devlocal.ParseTargets(lf.contexts, kc, protect)
	if err != nil {
		return nil, err
	}
	return &localPlan{hubURL: lf.hubURL, token: token, kubeconfig: kc, targets: targets}, nil
}

// runLocal starts one agent session per context and returns when all have
// stopped. A context that fails (for example expired credentials) is logged
// and does not stop the others.
func runLocal(ctx context.Context, lf *localFlags, getenv func(string) string, logger *slog.Logger) error {
	plan, err := planLocal(lf, getenv)
	if err != nil {
		return err
	}
	presets, err := localPresets(getenv)
	if err != nil {
		return fmt.Errorf("local mode: %s: %w", envPresets, err)
	}
	var namespaces []string
	for s := range strings.SplitSeq(getenv("EDDY_WATCH_NAMESPACES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			namespaces = append(namespaces, s)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, len(plan.targets))
	for i, t := range plan.targets {
		log := logger.With("context", t.Context)
		readOnly, reason := t.ReadOnly(lf.allowWrites, lf.allowWritesProtected)
		switch {
		case !readOnly && t.Protected:
			log.Warn("local mode: WRITES ARE ENABLED ON A PROTECTED CONTEXT (--allow-writes-protected); reconcile, suspend and resume run as your kubeconfig identity",
				"cluster", t.Cluster, "protect", lf.protect)
		case !readOnly:
			log.Warn("local mode: writes are enabled (--allow-writes); reconcile, suspend and resume run as your kubeconfig identity", "cluster", t.Cluster)
		default:
			log.Info("local mode: read-only", "cluster", t.Cluster, "reason", reason)
		}
		rc, err := devlocal.RestConfig(plan.kubeconfig, t.Context)
		if err != nil {
			errs[i] = err
			log.Error("local mode: context skipped", "error", err)
			continue
		}
		rc.UserAgent = "eddy-agent/" + version.Version + " (local)"
		cfg := &config.Agent{
			Cluster:       t.Cluster,
			HubURL:        plan.hubURL,
			Token:         plan.token,
			Namespaces:    namespaces,
			Presets:       presets,
			AllowInsecure: true,
			// The same defaults as config.LoadAgent. They still refuse
			// system: identities from the hub.
			AllowedGroupPrefixes: []string{"eddy:"},
			DenyUserPrefixes:     []string{"system:", "eks:", "kubernetes-admin"},
			// No health server: several sessions share this process.
			HealthAddr: "",
		}
		wg.Go(func() {
			err := agent.RunLocal(ctx, cfg, rc, log, agent.LocalOptions{Context: t.Context, ReadOnly: reason})
			if err != nil {
				errs[i] = fmt.Errorf("context %s: %w", t.Context, err)
				log.Error("local mode: context stopped", "error", err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
