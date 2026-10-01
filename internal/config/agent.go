package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/flux"
)

// Agent is configured entirely from environment variables (set by the
// eddy-agent chart), so no file mount is needed.
type Agent struct {
	Cluster string // EDDY_CLUSTER, must match the Cluster CR name
	HubURL  string // EDDY_HUB_URL, e.g. wss://eddy-agents.internal.example.com/agent/v1/connect
	Token   string // EDDY_AGENT_TOKEN
	// JoinToken is a one-time eddy_join_ token (EDDY_JOIN_TOKEN, ADR-0005).
	// The agent trades it for a permanent token on its first connection
	// and stores that in TokenSecret. Token may then be empty.
	JoinToken string
	// TokenSecret names the agent's own token Secret in Namespace
	// (EDDY_TOKEN_SECRET); TokenSecretKey is its key (EDDY_TOKEN_SECRET_KEY,
	// default "token"). The agent reads it for a token it stored earlier
	// and writes the token it receives at join time.
	TokenSecret    string
	TokenSecretKey string
	Namespace      string   // POD_NAMESPACE
	CAFile         string   // EDDY_HUB_CA_FILE, optional
	Namespaces     []string // EDDY_WATCH_NAMESPACES, comma-separated; empty = all
	// Presets are opt-in groups of kinds to watch (EDDY_WATCH_PRESETS,
	// comma-separated): "karpenter" and "externalSecrets".
	Presets              []string
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
	// Workload log streams (one request, several pods): MaxLogPods caps the
	// pods one stream follows (newest first) and LogLineRate its lines per
	// second across all of them; excess lines are dropped with a marker.
	MaxLogPods  int // EDDY_MAX_LOG_PODS, default 20
	LogLineRate int // EDDY_LOG_LINE_RATE, default 2000

	// Job history policy. The agent watches every Job but lists only active
	// ones, failed ones finished within JobFailedMaxAge and the newest
	// JobHistory finished ones per group; the rest fold into one Job
	// history row per namespace, which warns once it hides more than
	// JobBuildupThreshold Jobs.
	JobHistory          int           // EDDY_JOB_HISTORY, default 5
	JobFailedMaxAge     time.Duration // EDDY_JOB_FAILED_MAX_AGE, default 24h
	JobBuildupThreshold int           // EDDY_JOB_BUILDUP_THRESHOLD, default 100
}

// Agent limit defaults.
const (
	DefaultKubeQPS          = 20
	DefaultKubeBurst        = 40
	DefaultMaxConcurrent    = 16
	DefaultMaxLogStreams    = 8
	DefaultMaxSARConcurrent = 8
	DefaultMaxLogPods       = 20
	DefaultLogLineRate      = 2000

	DefaultJobHistory          = 5
	DefaultJobFailedMaxAge     = 24 * time.Hour
	DefaultJobBuildupThreshold = 100
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
	if a.MaxLogPods <= 0 {
		a.MaxLogPods = DefaultMaxLogPods
	}
	if a.LogLineRate <= 0 {
		a.LogLineRate = DefaultLogLineRate
	}
	if a.JobHistory <= 0 {
		a.JobHistory = DefaultJobHistory
	}
	if a.JobFailedMaxAge <= 0 {
		a.JobFailedMaxAge = DefaultJobFailedMaxAge
	}
	if a.JobBuildupThreshold <= 0 {
		a.JobBuildupThreshold = DefaultJobBuildupThreshold
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
		Token:                strings.TrimSpace(os.Getenv("EDDY_AGENT_TOKEN")),
		JoinToken:            strings.TrimSpace(os.Getenv("EDDY_JOIN_TOKEN")),
		TokenSecret:          os.Getenv("EDDY_TOKEN_SECRET"),
		TokenSecretKey:       os.Getenv("EDDY_TOKEN_SECRET_KEY"),
		Namespace:            os.Getenv("POD_NAMESPACE"),
		CAFile:               os.Getenv("EDDY_HUB_CA_FILE"),
		Namespaces:           list("EDDY_WATCH_NAMESPACES"),
		AllowedGroupPrefixes: list("EDDY_ALLOWED_GROUP_PREFIXES"),
		AllowedGroups:        list("EDDY_ALLOWED_GROUPS"),
		DenyUserPrefixes:     list("EDDY_DENY_USER_PREFIXES"),
		HealthAddr:           os.Getenv("EDDY_HEALTH_ADDR"),
	}
	a.AllowInsecure, _ = strconv.ParseBool(os.Getenv("EDDY_ALLOW_INSECURE"))
	var errs []error
	presets, err := flux.ParsePresets(list("EDDY_WATCH_PRESETS"))
	if err != nil {
		errs = append(errs, fmt.Errorf("EDDY_WATCH_PRESETS: %w", err))
	}
	a.Presets = presets
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
	positiveInt("EDDY_MAX_LOG_PODS", &a.MaxLogPods)
	positiveInt("EDDY_LOG_LINE_RATE", &a.LogLineRate)
	positiveInt("EDDY_JOB_HISTORY", &a.JobHistory)
	positiveInt("EDDY_JOB_BUILDUP_THRESHOLD", &a.JobBuildupThreshold)
	if v := strings.TrimSpace(os.Getenv("EDDY_JOB_FAILED_MAX_AGE")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("EDDY_JOB_FAILED_MAX_AGE must be a positive duration such as 24h, got %q", v))
		} else {
			a.JobFailedMaxAge = d
		}
	}
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
	if a.TokenSecretKey == "" {
		a.TokenSecretKey = "token"
	}
	if a.Token == "" && a.JoinToken == "" {
		errs = append(errs, errors.New("EDDY_AGENT_TOKEN (or EDDY_JOIN_TOKEN) is required"))
	}
	if a.JoinToken != "" {
		if !strings.HasPrefix(a.JoinToken, "eddy_join_") {
			errs = append(errs, errors.New("EDDY_JOIN_TOKEN must be an eddy_join_ token"))
		}
		if a.TokenSecret == "" || a.Namespace == "" {
			errs = append(errs, errors.New("EDDY_JOIN_TOKEN needs EDDY_TOKEN_SECRET and POD_NAMESPACE, where the agent stores its permanent token"))
		}
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
