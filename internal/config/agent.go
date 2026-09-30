package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Agent is configured entirely from environment variables (set by the
// eddy-agent chart), so no file mount is needed.
type Agent struct {
	Cluster              string   // EDDY_CLUSTER, must match the Cluster CR name
	HubURL               string   // EDDY_HUB_URL, e.g. wss://eddy-agents.internal.example.com/agent/v1/connect
	Token                string   // EDDY_AGENT_TOKEN
	CAFile               string   // EDDY_HUB_CA_FILE, optional
	Namespaces           []string // EDDY_WATCH_NAMESPACES, comma-separated; empty = all
	AllowedGroupPrefixes []string // EDDY_ALLOWED_GROUP_PREFIXES, default "eddy:"
	AllowedGroups        []string // EDDY_ALLOWED_GROUPS, optional exact allowlist
	DenyUserPrefixes     []string // EDDY_DENY_USER_PREFIXES, default "system:,eks:,kubernetes-admin"
	AllowInsecure        bool     // EDDY_ALLOW_INSECURE=1 permits ws:// (dev only)
	HealthAddr           string   // EDDY_HEALTH_ADDR, default ":8081"

	// Control-plane protection (ADR-0004). Every limit is per agent
	// process; the eddy-agent chart divides its limits.* values by the
	// replica count so the cluster sees the same total load.
	KubeQPS          float32 // EDDY_KUBE_QPS, default 20: client-go QPS, shared by the agent's own clients and, separately, by all impersonating clients
	KubeBurst        int     // EDDY_KUBE_BURST, default 40
	MaxConcurrent    int     // EDDY_MAX_CONCURRENT, default 16: user requests in flight
	MaxLogStreams    int     // EDDY_MAX_LOG_STREAMS, default 8: log streams in flight (part of MaxConcurrent)
	MaxSARConcurrent int     // EDDY_MAX_SAR_CONCURRENT, default 8: SubjectAccessReviews in flight
}

// Agent limit defaults.
const (
	DefaultKubeQPS          = 20
	DefaultKubeBurst        = 40
	DefaultMaxConcurrent    = 16
	DefaultMaxLogStreams    = 8
	DefaultMaxSARConcurrent = 8
)

// ApplyLimitDefaults fills unset limits with their defaults. LoadAgent calls
// it; callers that build an Agent themselves (local mode) call it too.
func (a *Agent) ApplyLimitDefaults() {
	if a.KubeQPS <= 0 {
		a.KubeQPS = DefaultKubeQPS
	}
	if a.KubeBurst <= 0 {
		a.KubeBurst = DefaultKubeBurst
	}
	if a.MaxConcurrent <= 0 {
		a.MaxConcurrent = DefaultMaxConcurrent
	}
	if a.MaxLogStreams <= 0 {
		a.MaxLogStreams = DefaultMaxLogStreams
	}
	if a.MaxSARConcurrent <= 0 {
		a.MaxSARConcurrent = DefaultMaxSARConcurrent
	}
}

// LoadAgent reads the agent configuration from the environment.
func LoadAgent() (*Agent, error) {
	list := func(k string) []string {
		var out []string
		for _, s := range strings.Split(os.Getenv(k), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	a := &Agent{
		Cluster:              os.Getenv("EDDY_CLUSTER"),
		HubURL:               os.Getenv("EDDY_HUB_URL"),
		Token:                os.Getenv("EDDY_AGENT_TOKEN"),
		CAFile:               os.Getenv("EDDY_HUB_CA_FILE"),
		Namespaces:           list("EDDY_WATCH_NAMESPACES"),
		AllowedGroupPrefixes: list("EDDY_ALLOWED_GROUP_PREFIXES"),
		AllowedGroups:        list("EDDY_ALLOWED_GROUPS"),
		DenyUserPrefixes:     list("EDDY_DENY_USER_PREFIXES"),
		HealthAddr:           os.Getenv("EDDY_HEALTH_ADDR"),
	}
	a.AllowInsecure, _ = strconv.ParseBool(os.Getenv("EDDY_ALLOW_INSECURE"))
	var errs []error
	positiveInt := func(k string, dst *int) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				errs = append(errs, fmt.Errorf("%s must be a positive integer, got %q", k, v))
				return
			}
			*dst = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EDDY_KUBE_QPS")); v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil || f <= 0 {
			errs = append(errs, fmt.Errorf("EDDY_KUBE_QPS must be a positive number, got %q", v))
		} else {
			a.KubeQPS = float32(f)
		}
	}
	positiveInt("EDDY_KUBE_BURST", &a.KubeBurst)
	positiveInt("EDDY_MAX_CONCURRENT", &a.MaxConcurrent)
	positiveInt("EDDY_MAX_LOG_STREAMS", &a.MaxLogStreams)
	positiveInt("EDDY_MAX_SAR_CONCURRENT", &a.MaxSARConcurrent)
	a.ApplyLimitDefaults()
	if len(a.AllowedGroupPrefixes) == 0 {
		a.AllowedGroupPrefixes = []string{"eddy:"}
	}
	if len(a.DenyUserPrefixes) == 0 {
		a.DenyUserPrefixes = []string{"system:", "eks:", "kubernetes-admin"}
	}
	if a.HealthAddr == "" {
		a.HealthAddr = ":8081"
	}
	if a.Cluster == "" {
		errs = append(errs, errors.New("EDDY_CLUSTER is required"))
	}
	if a.Token == "" {
		errs = append(errs, errors.New("EDDY_AGENT_TOKEN is required"))
	}
	switch {
	case strings.HasPrefix(a.HubURL, "wss://"):
	case strings.HasPrefix(a.HubURL, "ws://") && a.AllowInsecure:
	default:
		errs = append(errs, fmt.Errorf("EDDY_HUB_URL must be wss:// (ws:// needs EDDY_ALLOW_INSECURE=1), got %q", a.HubURL))
	}
	for _, p := range a.AllowedGroupPrefixes {
		if strings.HasPrefix(p, "system:") {
			errs = append(errs, errors.New(`EDDY_ALLOWED_GROUP_PREFIXES must not include the "system:" prefix`))
		}
	}
	return a, errors.Join(errs...)
}
