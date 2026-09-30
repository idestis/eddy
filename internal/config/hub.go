// Package config defines the hub and agent configuration files. The hub
// reads hub.yaml (rendered by the eddy-hub chart); ${ENV} references are
// expanded before parsing so secrets can come from environment variables.
package config

import (
	"errors"
	"fmt"
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

	Listen  Listen  `json:"listen"`
	Auth    Auth    `json:"auth"`
	Store   Store   `json:"store"`
	AI      AI      `json:"ai"`
	MCP     MCP     `json:"mcp"`
	Runtime Runtime `json:"runtime"`
	Dev     Dev     `json:"dev"`
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
	Driver    string    `json:"driver"`    // sqlite | memory
	Path      string    `json:"path"`      // sqlite file, default /var/lib/eddy/eddy.db
	Ephemeral bool      `json:"ephemeral"` // set by the chart when persistence is off
	Retention Retention `json:"retention"`
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

	def(&h.Store.Driver, "sqlite")
	def(&h.Store.Path, "/var/lib/eddy/eddy.db")
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
	case "sqlite", "memory":
	default:
		errs = append(errs, fmt.Errorf("store.driver %q is not supported (sqlite, memory)", h.Store.Driver))
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
	switch h.MCP.ProtectedClusters {
	case "confirm", "deny":
	default:
		errs = append(errs, errors.New("mcp.protectedClusters must be confirm or deny"))
	}
	return errors.Join(errs...)
}

// SecureCookies reports whether cookies must be Secure (__Host- prefix).
// Only plain-http localhost dev runs without it.
func (h *Hub) SecureCookies() bool { return strings.HasPrefix(h.PublicURL, "https://") }
