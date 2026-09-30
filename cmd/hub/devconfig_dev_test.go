//go:build dev

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/devlocal"
)

func TestDevConfig(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	arn := "arn:aws:eks:eu-west-2:123:cluster/prod-eu"
	err := os.WriteFile(kc, []byte(`apiVersion: v1
kind: Config
current-context: kind-eddy
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: u
  user: {token: fake}
contexts:
- name: kind-eddy
  context: {cluster: c, user: u}
- name: `+arn+`
  context: {cluster: c, user: u}
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kc)
	out := filepath.Join(dir, ".dev", "hub.yaml")
	tokenFile := filepath.Join(dir, ".dev", "agent-token")
	var stdout, stderr bytes.Buffer
	t.Setenv("EDDY_DATABASE_URL", "postgres://eddy@127.0.0.1:55432/eddy?sslmode=disable")
	code := devConfig([]string{"--contexts", "kind-eddy," + arn, "--out", out, "--token-file", tokenFile, "--postgres"},
		func(k string) string { return os.Getenv(k) }, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	tok, err := os.ReadFile(tokenFile)
	if err != nil || len(strings.TrimSpace(string(tok))) != 64 {
		t.Fatalf("token %q %v", tok, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(devlocal.TokenEnv, strings.TrimSpace(string(tok)))
	h, err := config.ParseHub(b)
	if err != nil {
		t.Fatalf("%s\n%v", b, err)
	}
	if len(h.StaticClusters) != 2 || h.StaticClusters[1].Name != "prod-eu" || !h.StaticClusters[1].Protected || h.StaticClusters[0].Protected {
		t.Fatalf("clusters %+v", h.StaticClusters)
	}
	if h.Store.Driver != "postgres" || h.Store.Postgres.DSNEnv != "EDDY_DATABASE_URL" || h.Peer.Listen != "" || h.AI.Enabled {
		t.Fatalf("store %+v ai %v", h.Store, h.AI.Enabled)
	}
	if !strings.Contains(stdout.String(), "PROTECTED") {
		t.Fatalf("summary: %s", stdout.String())
	}

	// Ask AI is explicit: a key alone does not enable it, EDDY_AI_PROVIDER does.
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-test"}
	if code := devConfig([]string{"--out", out, "--token-file", tokenFile}, func(k string) string { return env[k] }, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	b, _ = os.ReadFile(out)
	if h, err := config.ParseHub(b); err != nil || h.AI.Enabled {
		t.Fatalf("ai enabled without EDDY_AI_PROVIDER: %v", err)
	}
	env["EDDY_AI_PROVIDER"] = "anthropic"
	code = devConfig([]string{"--out", out, "--token-file", tokenFile}, func(k string) string { return env[k] }, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	b, _ = os.ReadFile(out)
	if h, err := config.ParseHub(b); err != nil || !h.AI.Enabled || h.AI.Provider != "anthropic" || len(h.StaticClusters) != 1 {
		t.Fatalf("ai config %v %+v", err, h)
	}

	stderr.Reset()
	bedrock := map[string]string{"EDDY_AI_PROVIDER": "bedrock"}
	if code := devConfig([]string{"--out", out, "--token-file", tokenFile}, func(k string) string { return bedrock[k] }, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "EDDY_BEDROCK_REGION") {
		t.Fatalf("bedrock without settings: code %d: %s", code, stderr.String())
	}
	if code := devConfig([]string{"--contexts", "missing", "--out", out, "--token-file", tokenFile}, func(string) string { return "" }, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown context: code %d", code)
	}
}
