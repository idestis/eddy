//go:build dev

package devlocal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Defaults shared by the agent's --local flags and `eddy-hub dev-config`.
const (
	// DefaultProtect matches contexts and cluster names that stay read-only
	// even with --allow-writes.
	DefaultProtect = `(?i)prod`
	// DefaultHubURL is the agent endpoint of the hub started by `task dev:hub`.
	DefaultHubURL = "ws://127.0.0.1:8443/agent/v1/connect"
	// TokenEnv carries the single shared agent token of a local setup.
	TokenEnv = "EDDY_DEV_AGENT_TOKEN"
	// DevModeEnv must be "1" for local mode (and fake login) to activate.
	DevModeEnv = "EDDY_DEV_MODE"
)

// Read-only reasons, returned to the UI as the 403 message of a write.
const (
	ReasonNoWrites  = "read-only local mode (start with --allow-writes)"
	reasonProtected = "read-only local mode: context %q matches --protect (start with --allow-writes-protected to override)"
)

// Target is one kubeconfig context served as one Eddy cluster.
type Target struct {
	Context   string // kubeconfig context name
	Cluster   string // Eddy cluster name (a DNS label)
	Protected bool   // Context or Cluster matches the --protect regex
}

// ReadOnly reports whether writes to t are refused and, if so, why.
// allowProtected only has an effect together with allowWrites.
func (t Target) ReadOnly(allowWrites, allowProtected bool) (bool, string) {
	switch {
	case !allowWrites:
		return true, ReasonNoWrites
	case t.Protected && !allowProtected:
		return true, fmt.Sprintf(reasonProtected, t.Context)
	}
	return false, ""
}

// CheckActivation enforces the runtime conditions of local mode (the build
// tag is enforced by this package existing at all): EDDY_DEV_MODE=1 and a hub
// URL on a loopback address.
func CheckActivation(getenv func(string) string, hubURL string) error {
	if getenv(DevModeEnv) != "1" {
		return fmt.Errorf("local mode needs %s=1", DevModeEnv)
	}
	return CheckLoopbackURL(hubURL)
}

// CheckLoopbackURL accepts ws:// or wss:// URLs whose host is localhost or a
// loopback IP (127.0.0.0/8, ::1).
func CheckLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("local mode: parse hub url: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("local mode: hub url must be ws:// or wss://, got %q", raw)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Unmap().IsLoopback() {
		return nil
	}
	return fmt.Errorf("local mode: hub url must point at a loopback address (127.0.0.1, localhost or [::1]), got %q", raw)
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ClusterName turns a kubeconfig context name into a DNS-label-safe cluster
// name. EKS ARN contexts ("arn:aws:eks:eu-west-2:123:cluster/prod-eu") use
// their last path segment ("prod-eu"). Other characters outside [a-z0-9-] are
// replaced with "-", runs of "-" collapse, and the result is at most 63
// characters.
func ClusterName(context string) (string, error) {
	s := strings.TrimSpace(context)
	if strings.HasPrefix(s, "arn:") {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if out == "" {
		return "", fmt.Errorf("local mode: context %q has no usable cluster name; rename it with %q", context, context+"=name")
	}
	return out, nil
}

// LoadKubeconfig reads kubeconfig without contacting any cluster. An empty
// path uses the standard rules: $KUBECONFIG (merged), then ~/.kube/config.
func LoadKubeconfig(path string) (*clientcmdapi.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	cfg, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("local mode: load kubeconfig: %w", err)
	}
	return cfg, nil
}

// RestConfig returns the client configuration of one context of cfg, with
// the kubeconfig's own credentials and no impersonation.
func RestConfig(cfg *clientcmdapi.Config, context string) (*rest.Config, error) {
	rc, err := clientcmd.NewNonInteractiveClientConfig(*cfg, context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("local mode: client config for context %q: %w", context, err)
	}
	return rc, nil
}

// ParseTargets resolves a --contexts value ("a,b=name,c") against cfg. An
// empty spec means the current context. Each entry is a context name,
// optionally renamed with "=name". Unknown contexts, duplicates, invalid names
// and two contexts mapping to the same cluster name are errors. protect, when
// set, marks targets whose context or cluster name matches it.
func ParseTargets(spec string, cfg *clientcmdapi.Config, protect *regexp.Regexp) ([]Target, error) {
	var entries []string
	for e := range strings.SplitSeq(spec, ",") {
		if e = strings.TrimSpace(e); e != "" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		if cfg.CurrentContext == "" {
			return nil, errors.New("local mode: kubeconfig has no current context; pass --contexts")
		}
		entries = []string{cfg.CurrentContext}
	}
	var out []Target
	byCluster := map[string]string{}
	var errs []error
	for _, e := range entries {
		ctx, name := e, ""
		if i := strings.LastIndex(e, "="); i >= 0 {
			ctx, name = strings.TrimSpace(e[:i]), strings.TrimSpace(e[i+1:])
			if !dnsLabel.MatchString(name) {
				errs = append(errs, fmt.Errorf("local mode: %q is not a valid cluster name (lowercase letters, digits and '-', at most 63)", name))
				continue
			}
		}
		if _, ok := cfg.Contexts[ctx]; !ok {
			errs = append(errs, fmt.Errorf("local mode: context %q is not in the kubeconfig", ctx))
			continue
		}
		if slices.ContainsFunc(out, func(t Target) bool { return t.Context == ctx }) {
			errs = append(errs, fmt.Errorf("local mode: context %q is listed twice", ctx))
			continue
		}
		if name == "" {
			n, err := ClusterName(ctx)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			name = n
		}
		if other, dup := byCluster[name]; dup {
			errs = append(errs, fmt.Errorf("local mode: contexts %q and %q both map to cluster %q; rename one with %q", other, ctx, name, ctx+"=other-name"))
			continue
		}
		byCluster[name] = ctx
		t := Target{Context: ctx, Cluster: name}
		t.Protected = protect != nil && (protect.MatchString(ctx) || protect.MatchString(name))
		out = append(out, t)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// CompileProtect compiles a --protect regex. An empty pattern protects nothing.
func CompileProtect(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("local mode: invalid --protect regex: %w", err)
	}
	return re, nil
}

// minTokenLen is the shortest agent token EnsureToken accepts from a file.
const minTokenLen = 32

// EnsureToken returns the dev agent token stored at path, creating it with
// 32 random bytes (hex) and mode 0600 when the file does not exist.
func EnsureToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(b))
		if len(tok) < minTokenLen {
			return "", fmt.Errorf("local mode: token in %s is shorter than %d characters; delete it to regenerate", path, minTokenLen)
		}
		return tok, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("local mode: read token: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("local mode: generate token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("local mode: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("local mode: write token: %w", err)
	}
	return tok, nil
}
