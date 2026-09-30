// Package config defines the hub and agent configuration files. The hub
// reads hub.yaml (rendered by the eddy-hub chart); ${ENV} references are
// expanded before parsing so secrets can come from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Hub is hub.yaml.
type Hub struct {
	// PublicURL is the browser-facing origin, e.g. https://eddy.internal.example.com.
	// Origin/Host checks and cookie security derive from it.
	PublicURL string `json:"publicURL"`
	// Namespace is where Cluster CRs' token Secrets live. Defaults to POD_NAMESPACE.
	Namespace string `json:"namespace"`
	// AgentsPublicURL is the base URL agents in workload clusters dial, e.g.
	// https://eddy-agents.internal.example.com. The install guide turns it
	// into wss://<host>/agent/v1/connect. Empty leaves a placeholder.
	AgentsPublicURL string `json:"agentsPublicURL,omitempty"`
	// Onboarding configures adding clusters from the UI (ADR-0005).
	Onboarding Onboarding `json:"onboarding"`

	Listen  Listen  `json:"listen"`
	Auth    Auth    `json:"auth"`
	Store   Store   `json:"store"`
	AI      AI      `json:"ai"`
	MCP     MCP     `json:"mcp"`
	Runtime Runtime `json:"runtime"`
	Dev     Dev     `json:"dev"`
	// Peer configures the channel between hub replicas (ADR-0004).
	Peer Peer `json:"peer"`
	// Clusters, when set, are used instead of watching Cluster CRs (local dev
	// without a management cluster). Tokens come from TokenEnv.
	StaticClusters []StaticCluster `json:"staticClusters,omitempty"`
}

type Listen struct {
	// UI serves the SPA, /api, /auth and /mcp. Default ":8080".
	UI string `json:"ui"`
	// Agents serves only /agent/v1/connect and /healthz. Default ":8443".
	Agents string `json:"agents"`
	// Metrics serves /metrics and /readyz. Default ":9090".
	Metrics string `json:"metrics"`
	// AgentTLS optionally terminates TLS on the agent listener.
	AgentTLS *TLS `json:"agentTLS,omitempty"`
}

type TLS struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

type Auth struct {
	Local            LocalAuth `json:"local"`
	Proxy            ProxyAuth `json:"proxy"`
	Groups           Groups    `json:"groups"`
	DenyUserPrefixes []string  `json:"denyUserPrefixes"`
	Session          Session   `json:"session"`
	LoginRateLimit   RateLimit `json:"loginRateLimit"`
	Tokens           Tokens    `json:"tokens"`
	// KeyFile holds ≥32 bytes of random data, treated as opaque bytes (trailing
	// whitespace trimmed). Sub-keys for CSRF, the PAT pepper and pre-session
	// cookies are derived from it with HKDF.
	KeyFile string `json:"keyFile"`
	// AuditViewerGroups (already prefixed, e.g. eddy:platform) may read everyone's audit events.
	AuditViewerGroups []string `json:"auditViewerGroups"`
}

type LocalAuth struct {
	Enabled    bool   `json:"enabled"`
	UsersFile  string `json:"usersFile"`  // users.yaml, hot-reloaded
	UserPrefix string `json:"userPrefix"` // default "local:"
}

type ProxyAuth struct {
	Enabled                  bool     `json:"enabled"`
	UserHeader               string   `json:"userHeader"`   // default X-Forwarded-Email
	GroupsHeader             string   `json:"groupsHeader"` // default X-Forwarded-Groups
	GroupsSeparator          string   `json:"groupsSeparator"`
	TrustedCIDRs             []string `json:"trustedCIDRs"`
	SharedSecretHeader       string   `json:"sharedSecretHeader"` // default X-Eddy-Proxy-Secret
	SharedSecretEnv          string   `json:"sharedSecretEnv"`    // default EDDY_PROXY_SECRET
	InsecureSkipSharedSecret bool     `json:"insecureSkipSharedSecret"`
	UserPrefix               string   `json:"userPrefix"`
}

type Groups struct {
	Prefix         string              `json:"prefix"`   // default "eddy:"
	AllUsers       string              `json:"allUsers"` // default "authenticated"
	Static         map[string][]string `json:"static"`   // identity → extra groups
	MaxGroups      int                 `json:"maxGroups"`
	MaxGroupLength int                 `json:"maxGroupLength"`
}

type Session struct {
	IdleTimeout     Duration `json:"idleTimeout"`     // default 8h
	AbsoluteTimeout Duration `json:"absoluteTimeout"` // default 24h
	MaxPerUser      int      `json:"maxPerUser"`      // default 10
}

type RateLimit struct {
	PerUsernameFailures int      `json:"perUsernameFailures"` // default 5
	Window              Duration `json:"window"`              // default 15m
	Lockout             Duration `json:"lockout"`             // default 15m
	PerIPPerMinute      int      `json:"perIPPerMinute"`      // default 20
	MaxConcurrentHashes int      `json:"maxConcurrentHashes"` // default 4
}

type Tokens struct {
	DefaultTTL       Duration `json:"defaultTTL"`       // default 720h (30d)
	MaxTTLLocal      Duration `json:"maxTTLLocal"`      // default 2160h (90d)
	MaxTTLProxy      Duration `json:"maxTTLProxy"`      // default 720h (30d)
	MaxPerUser       int      `json:"maxPerUser"`       // default 10
	ThreadWriteScope string   `json:"threadWriteScope"` // read | operate; default read
}

type Store struct {
	// Driver is postgres (the default, and the only one for production) or
	// memory. memory keeps everything in the process and loses it on
	// restart; it is meant for tests and local development (task dev).
	Driver    string    `json:"driver"`
	Postgres  Postgres  `json:"postgres"`
	Retention Retention `json:"retention"`
}

// Postgres configures the postgres store driver.
type Postgres struct {
	// DSNEnv names the environment variable holding the connection string
	// (a libpq URL or keyword/value string). Default EDDY_DATABASE_URL. The
	// DSN is never read from the config file because it may hold a password.
	DSNEnv string `json:"dsnEnv"`
	// MaxOpenConns caps the connection pool per replica. Default 10.
	MaxOpenConns int `json:"maxOpenConns"`
}

type Retention struct {
	AuditDays           int `json:"auditDays"`           // default 90
	ResolvedThreadsDays int `json:"resolvedThreadsDays"` // default 0 (keep)
	AskThreadsDays      int `json:"askThreadsDays"`      // default 30
}

type AI struct {
	Enabled   bool        `json:"enabled"`
	Provider  string      `json:"provider"` // anthropic | bedrock
	Anthropic AnthropicAI `json:"anthropic"`
	Bedrock   BedrockAI   `json:"bedrock"`
	AllowLogs bool        `json:"allowLogs"`
	Limits    AILimits    `json:"limits"`
}

type AnthropicAI struct {
	Model     string `json:"model"`     // default claude-haiku-4-5-20251001
	APIKeyEnv string `json:"apiKeyEnv"` // default ANTHROPIC_API_KEY
	BaseURL   string `json:"baseURL"`
}

type BedrockAI struct {
	Region    string           `json:"region"`
	ModelID   string           `json:"modelId"` // inference profile id or ARN
	Guardrail BedrockGuardrail `json:"guardrail"`
}

type BedrockGuardrail struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Trace   bool   `json:"trace"`
}

type AILimits struct {
	MaxRounds          int      `json:"maxRounds"`          // default 6
	MaxToolResultBytes int      `json:"maxToolResultBytes"` // default 24576
	MaxOutputTokens    int      `json:"maxOutputTokens"`    // default 1024
	Timeout            Duration `json:"timeout"`            // default 60s
	PerUserPerHour     int      `json:"perUserPerHour"`     // default 30
}

type MCP struct {
	Enabled           bool     `json:"enabled"`
	AllowedOrigins    []string `json:"allowedOrigins"`
	AllowLogs         bool     `json:"allowLogs"`
	Writes            bool     `json:"writes"`            // off unless set; the chart sets true. The runtime flag can only turn it off
	ProtectedClusters string   `json:"protectedClusters"` // confirm | deny; default confirm
	CallsPerMinute    int      `json:"callsPerMinute"`    // default 60
	WritesPerMinute   int      `json:"writesPerMinute"`   // default 10
	MaxResultBytes    int      `json:"maxResultBytes"`    // default 65536
}

// Peer configures the WebSocket between hub replicas that relays agent
// traffic (ADR-0004). A replica without peers (one replica, or local
// development) needs none of it.
type Peer struct {
	// Listen serves /peer/v1/connect only, e.g. ":8444" (the eddy-hub chart
	// sets it). Empty turns the peer channel off: fine for one replica,
	// but replicas then cannot relay agent traffic to each other.
	Listen string `json:"listen"`
	// Service is the DNS name of the headless Service that lists every hub
	// pod, resolved every 10 s. Empty turns discovery off: replicas then
	// dial only the owners of agent sessions they need.
	Service string `json:"service"`
	// PodName names this replica in agent_sessions and peer authentication.
	// Default $POD_NAME, then the hostname.
	PodName string `json:"podName"`
	// Advertise is the host:port other replicas dial to reach Listen.
	// Default $POD_IP with the Listen port.
	Advertise string `json:"advertise"`
}

// Onboarding configures the add-cluster wizard and join tokens. It needs
// Cluster CRs (it is off with staticClusters) and the hub's extra RBAC
// (the eddy-hub chart's onboarding.enabled).
type Onboarding struct {
	Enabled bool `json:"enabled"`
	// JoinTokenTTL is the default lifetime of a join token, default 1h. A
	// request may ask for up to MaxJoinTokenTTL.
	JoinTokenTTL Duration `json:"joinTokenTTL"`
	// AgentChart is the eddy-agent chart reference used in the install
	// guide, default oci://ghcr.io/idestis/charts/eddy-agent.
	AgentChart string `json:"agentChart"`
	// AgentChartVersion defaults to the hub's version.
	AgentChartVersion string `json:"agentChartVersion"`
	// AgentImage is the agent image for plain manifests, default
	// ghcr.io/idestis/eddy-agent:<AgentChartVersion>.
	AgentImage string `json:"agentImage"`
	// AgentNamespace is where the guide installs the agent, default eddy-system.
	AgentNamespace string `json:"agentNamespace"`
}

// MaxJoinTokenTTL caps a join token's lifetime (ADR-0005).
const MaxJoinTokenTTL = 24 * time.Hour

// Runtime points at the hot-reloaded kill-switch file (eddy-runtime ConfigMap).
type Runtime struct {
	FlagsFile string `json:"flagsFile"` // default /etc/eddy/runtime/flags.yaml
}

// Dev settings only take effect in binaries built with -tags dev.
type Dev struct {
	FakeLogin bool `json:"fakeLogin"`
}

type StaticCluster struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Environment string `json:"environment"`
	Color       string `json:"color"`
	Protected   bool   `json:"protected"`
	Order       int    `json:"order"`
	TokenEnv    string `json:"tokenEnv"`
}

// Duration is a time.Duration that (un)marshals as a Go duration string.
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return []byte(`"` + d.String() + `"`), nil }

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// LoadHub reads, expands, defaults and validates a hub config file.
func LoadHub(path string) (*Hub, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return ParseHub(b)
}

// ParseHub parses hub.yaml content.
func ParseHub(b []byte) (*Hub, error) {
	var h Hub
	if err := yaml.UnmarshalStrict([]byte(os.ExpandEnv(string(b))), &h); err != nil {
		return nil, fmt.Errorf("config: parse hub config: %w", err)
	}
	h.applyDefaults()
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return &h, nil
}

func (h *Hub) applyDefaults() {
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	defI := func(i *int, v int) {
		if *i == 0 {
			*i = v
		}
	}
	defD := func(d *Duration, v time.Duration) {
		if d.Duration == 0 {
			d.Duration = v
		}
	}
	def(&h.Namespace, os.Getenv("POD_NAMESPACE"))
	def(&h.Listen.UI, ":8080")
	def(&h.Listen.Agents, ":8443")
	def(&h.Listen.Metrics, ":9090")

	a := &h.Auth
	def(&a.Local.UserPrefix, "local:")
	def(&a.Proxy.UserHeader, "X-Forwarded-Email")
	def(&a.Proxy.GroupsHeader, "X-Forwarded-Groups")
	def(&a.Proxy.GroupsSeparator, ",")
	def(&a.Proxy.SharedSecretHeader, "X-Eddy-Proxy-Secret")
	def(&a.Proxy.SharedSecretEnv, "EDDY_PROXY_SECRET")
	def(&a.Groups.Prefix, "eddy:")
	def(&a.Groups.AllUsers, "authenticated")
	defI(&a.Groups.MaxGroups, 64)
	defI(&a.Groups.MaxGroupLength, 128)
	if a.DenyUserPrefixes == nil {
		a.DenyUserPrefixes = []string{"system:", "eks:", "kubernetes-admin"}
	}
	defD(&a.Session.IdleTimeout, 8*time.Hour)
	defD(&a.Session.AbsoluteTimeout, 24*time.Hour)
	defI(&a.Session.MaxPerUser, 10)
	defI(&a.LoginRateLimit.PerUsernameFailures, 5)
	defD(&a.LoginRateLimit.Window, 15*time.Minute)
	defD(&a.LoginRateLimit.Lockout, 15*time.Minute)
	defI(&a.LoginRateLimit.PerIPPerMinute, 20)
	defI(&a.LoginRateLimit.MaxConcurrentHashes, 4)
	defD(&a.Tokens.DefaultTTL, 30*24*time.Hour)
	defD(&a.Tokens.MaxTTLLocal, 90*24*time.Hour)
	defD(&a.Tokens.MaxTTLProxy, 30*24*time.Hour)
	defI(&a.Tokens.MaxPerUser, 10)
	def(&a.Tokens.ThreadWriteScope, "read")

	def(&h.Store.Driver, "postgres")
	def(&h.Store.Postgres.DSNEnv, "EDDY_DATABASE_URL")
	defI(&h.Store.Postgres.MaxOpenConns, 10)
	defI(&h.Store.Retention.AuditDays, 90)
	defI(&h.Store.Retention.AskThreadsDays, 30)

	def(&h.AI.Provider, "anthropic")
	def(&h.AI.Anthropic.Model, "claude-haiku-4-5-20251001")
	def(&h.AI.Anthropic.APIKeyEnv, "ANTHROPIC_API_KEY")
	defI(&h.AI.Limits.MaxRounds, 6)
	defI(&h.AI.Limits.MaxToolResultBytes, 24576)
	defI(&h.AI.Limits.MaxOutputTokens, 1024)
	defD(&h.AI.Limits.Timeout, 60*time.Second)
	defI(&h.AI.Limits.PerUserPerHour, 30)

	def(&h.MCP.ProtectedClusters, "confirm")
	defI(&h.MCP.CallsPerMinute, 60)
	defI(&h.MCP.WritesPerMinute, 10)
	defI(&h.MCP.MaxResultBytes, 64<<10)

	def(&h.Runtime.FlagsFile, "/etc/eddy/runtime/flags.yaml")

	defD(&h.Onboarding.JoinTokenTTL, time.Hour)
	def(&h.Onboarding.AgentChart, "oci://ghcr.io/idestis/charts/eddy-agent")
	def(&h.Onboarding.AgentNamespace, "eddy-system")

	def(&h.Peer.PodName, os.Getenv("POD_NAME"))
	if h.Peer.PodName == "" {
		if n, err := os.Hostname(); err == nil {
			h.Peer.PodName = n
		}
	}
	if h.Peer.Advertise == "" && h.Peer.Listen != "" {
		if ip := os.Getenv("POD_IP"); ip != "" {
			if _, port, err := net.SplitHostPort(h.Peer.Listen); err == nil {
				h.Peer.Advertise = net.JoinHostPort(ip, port)
			}
		}
	}
}

// Validate rejects unsafe or incomplete configurations.
func (h *Hub) Validate() error {
	var errs []error
	u, err := url.Parse(h.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("publicURL must be an absolute http(s) URL, got %q", h.PublicURL))
	}
	if !h.Auth.Local.Enabled && !h.Auth.Proxy.Enabled && !h.Dev.FakeLogin {
		errs = append(errs, errors.New("auth: enable at least one of auth.local or auth.proxy"))
	}
	if h.Auth.Local.Enabled && h.Auth.Local.UsersFile == "" {
		errs = append(errs, errors.New("auth.local.usersFile is required"))
	}
	if p := h.Auth.Proxy; p.Enabled {
		if len(p.TrustedCIDRs) == 0 {
			errs = append(errs, errors.New("auth.proxy.trustedCIDRs is required"))
		}
		for _, c := range p.TrustedCIDRs {
			if _, err := netip.ParsePrefix(c); err != nil {
				errs = append(errs, fmt.Errorf("auth.proxy.trustedCIDRs: %w", err))
			}
		}
		if !p.InsecureSkipSharedSecret && len(os.Getenv(p.SharedSecretEnv)) < 32 {
			errs = append(errs, fmt.Errorf("auth.proxy: %s must hold ≥32 bytes (or set insecureSkipSharedSecret)", p.SharedSecretEnv))
		}
	}
	if !strings.HasSuffix(h.Auth.Groups.Prefix, ":") {
		errs = append(errs, errors.New("auth.groups.prefix must end with ':'"))
	}
	switch h.Auth.Tokens.ThreadWriteScope {
	case "read", "operate":
	default:
		errs = append(errs, errors.New("auth.tokens.threadWriteScope must be read or operate"))
	}
	switch h.Store.Driver {
	case "postgres":
		if os.Getenv(h.Store.Postgres.DSNEnv) == "" {
			errs = append(errs, fmt.Errorf("store.postgres: %s must hold the database DSN", h.Store.Postgres.DSNEnv))
		}
		if h.Store.Postgres.MaxOpenConns < 1 {
			errs = append(errs, errors.New("store.postgres.maxOpenConns must be at least 1"))
		}
	case "memory":
	default:
		errs = append(errs, fmt.Errorf("store.driver %q is not supported (postgres, memory)", h.Store.Driver))
	}
	if h.Peer.Listen != "" {
		if _, _, err := net.SplitHostPort(h.Peer.Listen); err != nil {
			errs = append(errs, fmt.Errorf("peer.listen: %w", err))
		}
		if h.Peer.PodName == "" {
			errs = append(errs, errors.New("peer.podName (or POD_NAME) is required with peer.listen"))
		}
		if h.Peer.Advertise != "" {
			if _, _, err := net.SplitHostPort(h.Peer.Advertise); err != nil {
				errs = append(errs, fmt.Errorf("peer.advertise: %w", err))
			}
		}
	}
	if h.AI.Enabled {
		switch h.AI.Provider {
		case "anthropic":
		case "bedrock":
			if h.AI.Bedrock.Region == "" || h.AI.Bedrock.ModelID == "" {
				errs = append(errs, errors.New("ai.bedrock.region and ai.bedrock.modelId are required"))
			}
		default:
			errs = append(errs, fmt.Errorf("ai.provider %q is not supported (anthropic, bedrock)", h.AI.Provider))
		}
	}
	if h.AgentsPublicURL != "" {
		u, err := url.Parse(h.AgentsPublicURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "wss" && u.Scheme != "ws") {
			errs = append(errs, fmt.Errorf("agentsPublicURL must be an absolute https:// (or wss://) URL, got %q", h.AgentsPublicURL))
		}
	}
	if d := h.Onboarding.JoinTokenTTL.Duration; d < time.Minute || d > MaxJoinTokenTTL {
		errs = append(errs, fmt.Errorf("onboarding.joinTokenTTL must be between 1m and %s", MaxJoinTokenTTL))
	}
	switch h.MCP.ProtectedClusters {
	case "confirm", "deny":
	default:
		errs = append(errs, errors.New("mcp.protectedClusters must be confirm or deny"))
	}
	return errors.Join(errs...)
}

// EphemeralStore reports whether hub data is lost on restart (the memory
// store). The hub then logs a warning and /api/v1/me sets
// features.ephemeralStore.
func (h *Hub) EphemeralStore() bool { return h.Store.Driver == "memory" }

// SecureCookies reports whether cookies must be Secure (__Host- prefix).
// Only plain-http localhost dev runs without it.
func (h *Hub) SecureCookies() bool { return strings.HasPrefix(h.PublicURL, "https://") }
