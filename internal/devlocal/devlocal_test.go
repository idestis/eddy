//go:build dev

package devlocal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/idestis/eddy/internal/config"
)

func TestClusterName(t *testing.T) {
	long := strings.Repeat("a", 70)
	tests := []struct {
		in, want string
		err      bool
	}{
		{in: "kind-eddy", want: "kind-eddy"},
		{in: "arn:aws:eks:eu-west-2:123456789012:cluster/prod-eu", want: "prod-eu"},
		{in: "arn:aws:eks:us-east-1:123:cluster/Staging_Main", want: "staging-main"},
		{in: "gke_my-project_europe-west1-b_dev", want: "gke-my-project-europe-west1-b-dev"},
		{in: "admin@Dev.Cluster", want: "admin-dev-cluster"},
		{in: "--weird--", want: "weird"},
		{in: long, want: strings.Repeat("a", 63)},
		{in: strings.Repeat("a", 62) + "-b", want: strings.Repeat("a", 62)},
		{in: "@@@", err: true},
		{in: "", err: true},
	}
	for _, tt := range tests {
		got, err := ClusterName(tt.in)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("ClusterName(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.err)
		}
		if err == nil && !dnsLabel.MatchString(got) {
			t.Errorf("ClusterName(%q) = %q is not a DNS label", tt.in, got)
		}
	}
}

func kubeconfig(current string, contexts ...string) *clientcmdapi.Config {
	c := clientcmdapi.NewConfig()
	c.CurrentContext = current
	for _, n := range contexts {
		c.Contexts[n] = &clientcmdapi.Context{Cluster: n, AuthInfo: n}
	}
	return c
}

func TestParseTargets(t *testing.T) {
	prodARN := "arn:aws:eks:eu-west-2:123:cluster/prod-eu"
	kc := kubeconfig("kind-eddy", "kind-eddy", prodARN, "staging", "Staging", "dev_1", "dev-1")
	protect := regexp.MustCompile(DefaultProtect)
	tests := []struct {
		name string
		spec string
		kc   *clientcmdapi.Config
		want []Target
		err  string
	}{
		{name: "current context", spec: "", want: []Target{{Context: "kind-eddy", Cluster: "kind-eddy"}}},
		{name: "arn and protect", spec: "kind-eddy, " + prodARN, want: []Target{
			{Context: "kind-eddy", Cluster: "kind-eddy"},
			{Context: prodARN, Cluster: "prod-eu", Protected: true},
		}},
		{name: "rename", spec: "staging=stg", want: []Target{{Context: "staging", Cluster: "stg"}}},
		{name: "protect matches the new name", spec: "staging=production", want: []Target{{Context: "staging", Cluster: "production", Protected: true}}},
		{name: "rename resolves collision", spec: "staging,Staging=staging-2", want: []Target{
			{Context: "staging", Cluster: "staging"}, {Context: "Staging", Cluster: "staging-2"},
		}},
		{name: "collision", spec: "dev_1,dev-1", err: `both map to cluster "dev-1"`},
		{name: "case collision", spec: "staging,Staging", err: "both map to cluster"},
		{name: "unknown", spec: "nope", err: `context "nope" is not in the kubeconfig`},
		{name: "duplicate", spec: "staging,staging", err: "listed twice"},
		{name: "invalid rename", spec: "staging=Bad_Name", err: "not a valid cluster name"},
		{name: "no current context", spec: " , ", kc: kubeconfig("", "a"), err: "no current context"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := kc
			if tt.kc != nil {
				c = tt.kc
			}
			got, err := ParseTargets(tt.spec, c, protect)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("want error containing %q, got %v", tt.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %+v, want %+v", got, tt.want)
				}
			}
		})
	}
	// Without a protect regex nothing is protected.
	got, err := ParseTargets(prodARN, kc, nil)
	if err != nil || got[0].Protected {
		t.Fatalf("nil protect: %+v %v", got, err)
	}
}

func TestActivation(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == DevModeEnv {
				return v
			}
			return ""
		}
	}
	tests := []struct {
		devMode string
		url     string
		ok      bool
	}{
		{"1", "ws://127.0.0.1:8443/agent/v1/connect", true},
		{"1", "ws://localhost:8443/agent/v1/connect", true},
		{"1", "ws://[::1]:8443/agent/v1/connect", true},
		{"1", "wss://127.0.0.2:8443/agent/v1/connect", true},
		{"", "ws://127.0.0.1:8443/agent/v1/connect", false},
		{"0", "ws://127.0.0.1:8443/agent/v1/connect", false},
		{"true", "ws://127.0.0.1:8443/agent/v1/connect", false},
		{"1", "wss://eddy.example.com/agent/v1/connect", false},
		{"1", "ws://10.0.0.1:8443/agent/v1/connect", false},
		{"1", "ws://0.0.0.0:8443/agent/v1/connect", false},
		{"1", "ws://localhost.example.com/agent/v1/connect", false},
		{"1", "http://127.0.0.1:8443/agent/v1/connect", false},
		{"1", "ws://[::ffff:10.0.0.1]:8443/", false},
		{"1", "://bad", false},
	}
	for _, tt := range tests {
		err := CheckActivation(env(tt.devMode), tt.url)
		if (err == nil) != tt.ok {
			t.Errorf("EDDY_DEV_MODE=%q url=%q: err=%v, want ok=%v", tt.devMode, tt.url, err, tt.ok)
		}
	}
}

func TestReadOnly(t *testing.T) {
	plain := Target{Context: "kind-eddy", Cluster: "kind-eddy"}
	prod := Target{Context: "prod", Cluster: "prod", Protected: true}
	tests := []struct {
		t                     Target
		writes, writesProtect bool
		readOnly              bool
		reason                string
	}{
		{plain, false, false, true, "--allow-writes"},
		{plain, false, true, true, "--allow-writes"},
		{plain, true, false, false, ""},
		{prod, false, false, true, "--allow-writes)"},
		{prod, true, false, true, "--allow-writes-protected"},
		{prod, false, true, true, "--allow-writes)"},
		{prod, true, true, false, ""},
	}
	for _, tt := range tests {
		ro, reason := tt.t.ReadOnly(tt.writes, tt.writesProtect)
		if ro != tt.readOnly || !strings.Contains(reason, tt.reason) || (!ro && reason != "") {
			t.Errorf("%s writes=%v protected=%v: got %v %q", tt.t.Context, tt.writes, tt.writesProtect, ro, reason)
		}
	}
}

func TestEnsureToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent-token")
	a, err := EnsureToken(path)
	if err != nil || len(a) != 64 {
		t.Fatalf("token %q %v", a, err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	b, err := EnsureToken(path)
	if err != nil || a != b {
		t.Fatalf("second call %q %v", b, err)
	}
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureToken(path); err == nil {
		t.Fatal("short token accepted")
	}
}

func TestRenderHubParses(t *testing.T) {
	t.Setenv(TokenEnv, strings.Repeat("x", 64))
	t.Setenv("EDDY_DATABASE_URL", "postgres://eddy@127.0.0.1:55432/eddy?sslmode=disable")
	targets := []Target{
		{Context: "kind-eddy", Cluster: "kind-eddy"},
		{Context: "arn:aws:eks:eu-west-2:123:cluster/prod-eu", Cluster: "prod-eu", Protected: true},
		{Context: `we"ird: ctx`, Cluster: "weird"},
		{Context: "ctx-$HOME", Cluster: "ctx-home"},
	}
	t.Setenv("GITHUB_CLIENT_SECRET", "s")
	for _, o := range []HubOptions{
		{Targets: targets, UsersFile: "hack/users.dev.yaml", KeyFile: "hack/dev.key"},
		{Targets: targets, UsersFile: "hack/users.dev.yaml", KeyFile: "hack/dev.key", GitHub: GitHubOptions{
			ClientID: "Iv23li", Organizations: []string{"acme", `we"ird`}, Teams: []string{"acme/platform"},
		}},
		{Targets: targets, UsersFile: "hack/users.dev.yaml", KeyFile: "hack/dev.key", Postgres: true, AI: AIOptions{Provider: "anthropic", AnthropicModel: "claude-x"}},
		{Targets: targets, UsersFile: "hack/users.dev.yaml", KeyFile: "hack/dev.key", AI: AIOptions{
			Provider: "bedrock", BedrockRegion: "eu-west-2", BedrockModelID: "eu.anthropic.claude-haiku-4-5-20251001-v1:0",
			GuardrailID: "gr-1", GuardrailVersion: "1",
		}},
	} {
		h, err := config.ParseHub(RenderHub(o))
		if err != nil {
			t.Fatalf("%s\n%v", RenderHub(o), err)
		}
		if !h.Dev.FakeLogin || h.PublicURL != "http://localhost:5173" || !h.MCP.Enabled || !h.Auth.Local.Enabled {
			t.Fatalf("unexpected config %+v", h)
		}
		for _, l := range []string{h.Listen.UI, h.Listen.Agents, h.Listen.Metrics} {
			if !strings.HasPrefix(l, "127.0.0.1:") {
				t.Fatalf("listener %q is not loopback", l)
			}
		}
		wantDriver := "memory"
		if o.Postgres {
			wantDriver = "postgres"
		}
		if h.Store.Driver != wantDriver || h.AI.Enabled != (o.AI.Provider != "") || (o.AI.Provider != "" && h.AI.Provider != o.AI.Provider) {
			t.Fatalf("store %q ai %v/%q", h.Store.Driver, h.AI.Enabled, h.AI.Provider)
		}
		switch o.AI.Provider {
		case "anthropic":
			if h.AI.Anthropic.Model != "claude-x" || h.AI.Anthropic.APIKeyEnv != "ANTHROPIC_API_KEY" {
				t.Fatalf("anthropic %+v", h.AI.Anthropic)
			}
		case "bedrock":
			if b := h.AI.Bedrock; b.Region != "eu-west-2" || b.ModelID != o.AI.BedrockModelID || b.Guardrail.ID != "gr-1" || b.Guardrail.Version != "1" {
				t.Fatalf("bedrock %+v", b)
			}
		}
		if gh := h.Auth.GitHub; gh.Enabled != (o.GitHub.ClientID != "") ||
			(gh.Enabled && (gh.ClientID != o.GitHub.ClientID || gh.ClientSecretEnv != "GITHUB_CLIENT_SECRET" ||
				strings.Join(gh.AllowedOrganizations, ",") != strings.Join(o.GitHub.Organizations, ",") ||
				strings.Join(gh.AllowedTeams, ",") != strings.Join(o.GitHub.Teams, ","))) {
			t.Fatalf("github %+v", gh)
		}
		if len(h.StaticClusters) != len(targets) {
			t.Fatalf("clusters %+v", h.StaticClusters)
		}
		for i, c := range h.StaticClusters {
			if c.Name != targets[i].Cluster || c.Protected != targets[i].Protected || c.TokenEnv != TokenEnv || c.Color == "" {
				t.Fatalf("cluster %d: %+v", i, c)
			}
			if !strings.Contains(c.Environment, strings.ReplaceAll(targets[i].Context, "$", "")) {
				t.Fatalf("environment %q", c.Environment)
			}
		}
	}
}

func TestAIFromEnv(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want AIOptions
		err  string
	}{
		// The mere presence of credentials does not turn Ask AI on.
		{env: map[string]string{"ANTHROPIC_API_KEY": "sk", "AWS_PROFILE": "p"}, want: AIOptions{}},
		{env: map[string]string{"EDDY_AI_PROVIDER": "anthropic"}, err: "needs ANTHROPIC_API_KEY"},
		{env: map[string]string{"EDDY_AI_PROVIDER": "anthropic", "ANTHROPIC_API_KEY": "sk", "EDDY_AI_MODEL": "m"},
			want: AIOptions{Provider: "anthropic", AnthropicModel: "m"}},
		{env: map[string]string{"EDDY_AI_PROVIDER": "bedrock", "AWS_PROFILE": "p"}, err: "EDDY_BEDROCK_REGION, EDDY_BEDROCK_MODEL_ID"},
		{env: map[string]string{"EDDY_AI_PROVIDER": "bedrock", "EDDY_BEDROCK_REGION": "eu-west-2", "EDDY_BEDROCK_MODEL_ID": "m", "EDDY_BEDROCK_GUARDRAIL_ID": "g"},
			err: "GUARDRAIL_VERSION"},
		{env: map[string]string{"EDDY_AI_PROVIDER": "bedrock", "EDDY_BEDROCK_REGION": "eu-west-2", "EDDY_BEDROCK_MODEL_ID": "m"},
			want: AIOptions{Provider: "bedrock", BedrockRegion: "eu-west-2", BedrockModelID: "m"}},
		{env: map[string]string{"EDDY_AI_PROVIDER": "openai"}, err: "not supported"},
	}
	for _, tt := range tests {
		got, err := AIFromEnv(func(k string) string { return tt.env[k] })
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%v: want error %q, got %v", tt.env, tt.err, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%v: got %+v %v, want %+v", tt.env, got, err, tt.want)
		}
	}
}

func TestGitHubFromEnv(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want GitHubOptions
		err  string
	}{
		{env: map[string]string{"GITHUB_CLIENT_SECRET": "s", "EDDY_GITHUB_ORGS": "acme"}, want: GitHubOptions{}},
		{env: map[string]string{"EDDY_GITHUB_CLIENT_ID": "c", "EDDY_GITHUB_ORGS": "acme"}, err: "needs GITHUB_CLIENT_SECRET"},
		{env: map[string]string{"EDDY_GITHUB_CLIENT_ID": "c", "GITHUB_CLIENT_SECRET": "s"}, err: "needs EDDY_GITHUB_ORGS"},
		{env: map[string]string{"EDDY_GITHUB_CLIENT_ID": "c", "GITHUB_CLIENT_SECRET": "s", "EDDY_GITHUB_ALLOW_ALL": "1"},
			want: GitHubOptions{ClientID: "c", AllowAll: true}},
		{env: map[string]string{"EDDY_GITHUB_CLIENT_ID": " c ", "GITHUB_CLIENT_SECRET": "s", "EDDY_GITHUB_ORGS": "acme, beta,", "EDDY_GITHUB_TEAMS": "acme/sre", "EDDY_GITHUB_BASE_URL": "https://ghe.example.com"},
			want: GitHubOptions{ClientID: "c", Organizations: []string{"acme", "beta"}, Teams: []string{"acme/sre"}, BaseURL: "https://ghe.example.com"}},
	}
	for _, tt := range tests {
		got, err := GitHubFromEnv(func(k string) string { return tt.env[k] })
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%v: want error %q, got %v", tt.env, tt.err, err)
			}
			continue
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("%v: got %+v %v, want %+v", tt.env, got, err, tt.want)
		}
	}
}
