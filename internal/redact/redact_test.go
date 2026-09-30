package redact

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string // exact output, when set
		gone    []string
		kept    []string
		minMask int
	}{
		{
			name:    "jwt",
			in:      "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U here",
			gone:    []string{"eyJhbGci", "dozjgNry"},
			kept:    []string{"[REDACTED:jwt]", " here"},
			minMask: 1,
		},
		{
			name: "aws access key",
			in:   "creds AKIAIOSFODNN7EXAMPLE and ASIAY34FZKBOKMUTVV7A",
			want: "creds [REDACTED:aws_key] and [REDACTED:aws_key]",
		},
		{
			name: "pem private key",
			in:   "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1\nabc==\n-----END RSA PRIVATE KEY-----\nafter",
			want: "key:\n[REDACTED:pem]\nafter",
		},
		{
			name:    "truncated pem",
			in:      "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ",
			gone:    []string{"b3BlbnNz"},
			minMask: 1,
		},
		{
			name: "eddy pat",
			in:   "use eddy_pat_abcdefghijkl0123456789abcdefghijklmnopqrstuvwxyzAB please",
			want: "use [REDACTED:eddy_pat] please",
		},
		{
			name: "github tokens",
			in:   "ghp_0123456789abcdefghijABCDEFGHIJ012345 github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
			want: "[REDACTED:github_token] [REDACTED:github_token]",
		},
		{
			name: "slack token",
			in:   "xoxb-123456789012-1234567890123-AbCdEfGhIjKl",
			want: "[REDACTED:slack_token]",
		},
		{
			name: "anthropic key",
			in:   "ANTHROPIC=sk-ant-api03-abcdefghijklmnop_qrstu-vwxyz",
			gone: []string{"sk-ant-api03"},
		},
		{
			name: "bearer header",
			in:   "Authorization: Bearer abc.def-123_ghi",
			want: "Authorization: Bearer [REDACTED:auth]",
		},
		{
			name: "basic header",
			in:   "authorization: Basic dXNlcjpwYXNzd29yZA==",
			want: "authorization: Basic [REDACTED:auth]",
		},
		{
			name: "prose about basic auth is kept",
			in:   "uses basic authentication and a bearer token",
			want: "uses basic authentication and a bearer token",
		},
		{
			name: "url with password",
			in:   "clone https://bob:hunter2@git.example.com/org/repo.git failed",
			want: "clone https://[REDACTED:userinfo]@git.example.com/org/repo.git failed",
		},
		{
			name: "ssh url without password is kept",
			in:   "url: ssh://git@github.com/eddy-gitops/fleet",
			want: "url: ssh://git@github.com/eddy-gitops/fleet",
		},
		{
			name: "password key value",
			in:   "password=hunter2 DB_PASSWORD: s3cr3t apiKey: 'abc123' \"client_secret\": \"xyz\"",
			want: "password=[REDACTED:secret] DB_PASSWORD: [REDACTED:secret] apiKey: '[REDACTED:secret]' \"client_secret\": \"[REDACTED:secret]\"",
		},
		{
			name: "query string token",
			in:   "GET /hook?token=abc123&x=1",
			want: "GET /hook?token=[REDACTED:secret]&x=1",
		},
		{
			name: "secret references are kept",
			in:   "secretRef: {name: git-creds} tokenSecretName: gh passwordFile: /etc/pw",
			want: "secretRef: {name: git-creds} tokenSecretName: gh passwordFile: /etc/pw",
		},
		{
			name: "error prose is kept",
			in:   `Secret "flux-system/git" not found; secret: not found`,
			want: `Secret "flux-system/git" not found; secret: not found`,
		},
		{
			name: "flux sha1 revision kept",
			in:   "Applied revision: main@sha1:5c5a2d6c0b39f6f4a0a4c3d2b1e0f9a8b7c6d5e4",
			want: "Applied revision: main@sha1:5c5a2d6c0b39f6f4a0a4c3d2b1e0f9a8b7c6d5e4",
		},
		{
			name: "image digest kept",
			in:   "image: ghcr.io/stefanprodan/podinfo@sha256:0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9",
			want: "image: ghcr.io/stefanprodan/podinfo@sha256:0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9",
		},
		{
			name: "oci digest revision kept",
			in:   "revision 6.5.0@sha256:0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9",
			want: "revision 6.5.0@sha256:0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9",
		},
		{
			name: "legacy flux revision kept",
			in:   "main/5c5a2d6c0b39f6f4a0a4c3d2b1e0f9a8b7c6d5e4",
			want: "main/5c5a2d6c0b39f6f4a0a4c3d2b1e0f9a8b7c6d5e4",
		},
		{
			name: "bare hex secret masked",
			in:   "key 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			want: "key [REDACTED:high_entropy]",
		},
		{
			name: "random base62 masked",
			in:   "value Zx8Qp2LmN4vR7tY1wK9sD3fG6hJ0aB5c",
			want: "value [REDACTED:high_entropy]",
		},
		{
			name: "kubernetes uid kept",
			in:   "uid: 3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			want: "uid: 3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		},
		{
			name: "long object names kept",
			in:   "pod flux-system-kustomize-controller-6c8d9f7b5-x2k4l and kustomize.toolkit.fluxcd.io/reconcile-requested-at",
			want: "pod flux-system-kustomize-controller-6c8d9f7b5-x2k4l and kustomize.toolkit.fluxcd.io/reconcile-requested-at",
		},
		{
			name: "long condition reason kept",
			in:   "reason: ReconciliationSucceededAfterRetryingDependency",
			want: "reason: ReconciliationSucceededAfterRetryingDependency",
		},
		{
			name: "plain message kept",
			in:   "HelmRelease podinfo upgrade succeeded: chart podinfo@6.5.0",
			want: "HelmRelease podinfo upgrade succeeded: chart podinfo@6.5.0",
		},
		{
			name: "long resource ids kept",
			in:   "helm.toolkit.fluxcd.io/HelmRelease/monitoring/kube-prometheus-stack2 apps/Deployment/kube-system/coredns-v1-28-4-eksbuild2 Deployment/default/podinfo-7d9b8c6f5d-2xq9z",
			want: "helm.toolkit.fluxcd.io/HelmRelease/monitoring/kube-prometheus-stack2 apps/Deployment/kube-system/coredns-v1-28-4-eksbuild2 Deployment/default/podinfo-7d9b8c6f5d-2xq9z",
		},
		{
			name: "arn and camel case names kept",
			in:   "arn:aws:iam::123456789012:role/EddyBedrockAccessRoleForProduction2 ReconciliationSucceededAfterRetrying2",
			want: "arn:aws:iam::123456789012:role/EddyBedrockAccessRoleForProduction2 ReconciliationSucceededAfterRetrying2",
		},
		{
			name: "bedrock model id kept",
			in:   "modelId: eu.anthropic.claude-haiku-4-5-20251001-v1:0",
			want: "modelId: eu.anthropic.claude-haiku-4-5-20251001-v1:0",
		},
		{
			name: "base64 secret with slashes masked",
			in:   "key wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY9x",
			want: "key [REDACTED:high_entropy]",
		},
		{
			name: "random key inside a path masked",
			in:   "https://hooks.example.com/services/T00000000/Zx8Qp2LmN4vR7tY1wK9sD3fG6hJ0aB5c",
			want: "https://hooks.example.com/services/T00000000/[REDACTED:high_entropy]",
		},
		{
			name: "no double masking",
			in:   "token: ghp_0123456789abcdefghijABCDEFGHIJ012345",
			want: "token: [REDACTED:github_token]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := Text(tt.in)
			if tt.want != "" && got != tt.want {
				t.Errorf("Text()\n got: %q\nwant: %q", got, tt.want)
			}
			for _, g := range tt.gone {
				if strings.Contains(got, g) {
					t.Errorf("output still contains %q: %q", g, got)
				}
			}
			for _, k := range tt.kept {
				if !strings.Contains(got, k) {
					t.Errorf("output lost %q: %q", k, got)
				}
			}
			wantN := strings.Count(got, maskPrefix)
			if n != wantN {
				t.Errorf("count = %d, masks in output = %d", n, wantN)
			}
			if n < tt.minMask {
				t.Errorf("count = %d, want >= %d", n, tt.minMask)
			}
		})
	}
}

func TestYAML(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		wantN int
	}{
		{
			name: "secret data and stringData dropped",
			in: `apiVersion: v1
kind: Secret
metadata:
  name: git
data:
  username: Ym9i
  password: aHVudGVyMg==
stringData:
  token: plain
type: Opaque`,
			want: `apiVersion: v1
kind: Secret
metadata:
  name: git
data: [REDACTED:data]
stringData: [REDACTED:data]
type: Opaque`,
			wantN: 2,
		},
		{
			name:  "inline data map dropped",
			in:    "data: {a: b}\nkind: ConfigMap",
			want:  "data: [REDACTED:data]\nkind: ConfigMap",
			wantN: 1,
		},
		{
			name: "env values dropped, names and valueFrom kept",
			in: `spec:
  containers:
  - name: app
    env:
    - name: DB_HOST
      value: db.internal
    - name: DB_PASSWORD
      valueFrom:
        secretKeyRef:
          name: db
          key: password
    - name: MULTI
      value: |
        line one
        line two
    image: ghcr.io/org/app:1.2.3`,
			want: `spec:
  containers:
  - name: app
    env:
    - name: DB_HOST
      value: [REDACTED:env]
    - name: DB_PASSWORD
      valueFrom:
        secretKeyRef:
          name: db
          key: password
    - name: MULTI
      value: [REDACTED:env]
    image: ghcr.io/org/app:1.2.3`,
			wantN: 2,
		},
		{
			name: "env at same indent as key",
			in: `env:
- name: A
  value: "x"
ports:
- value: 80`,
			want: `env:
- name: A
  value: [REDACTED:env]
ports:
- value: 80`,
			wantN: 1,
		},
		{
			name: "last applied annotation dropped",
			in: `metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"v1","data":{"password":"x"}}
    fluxcd.io/keep: "yes"
  name: x`,
			want: `metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: [REDACTED:last_applied]
    fluxcd.io/keep: "yes"
  name: x`,
			wantN: 1,
		},
		{
			name: "block scalar under secret key dropped",
			in: `values:
  privateKey: |
    abc
    def
  replicas: 2`,
			want: `values:
  privateKey: [REDACTED:secret]
  replicas: 2`,
			wantN: 1,
		},
		{
			name: "flux kustomization kept intact",
			in: `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps
  namespace: flux-system
spec:
  interval: 10m
  path: ./apps/production
  sourceRef:
    kind: GitRepository
    name: fleet
  decryption:
    provider: sops
    secretRef:
      name: sops-age
status:
  lastAppliedRevision: main@sha1:5c5a2d6c0b39f6f4a0a4c3d2b1e0f9a8b7c6d5e4`,
			wantN: 0,
		},
		{
			name:  "text rules still apply",
			in:    "spec:\n  url: https://u:p4ss@example.com/repo",
			want:  "spec:\n  url: https://[REDACTED:userinfo]@example.com/repo",
			wantN: 1,
		},
		{
			name:  "malformed yaml does not fail",
			in:    ":\n  - : :\n\t\tdata\n",
			want:  ":\n  - : :\n\t\tdata\n",
			wantN: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := YAML(tt.in)
			want := tt.want
			if want == "" {
				want = tt.in
			}
			if got != want {
				t.Errorf("YAML()\n got:\n%s\nwant:\n%s", got, want)
			}
			if n != tt.wantN {
				t.Errorf("count = %d, want %d", n, tt.wantN)
			}
		})
	}
}

func TestEntropy(t *testing.T) {
	if e := entropy("aaaa"); e != 0 {
		t.Errorf("entropy(aaaa) = %v", e)
	}
	if e := entropy("abcd"); e != 2 {
		t.Errorf("entropy(abcd) = %v", e)
	}
}

func FuzzText(f *testing.F) {
	f.Add("password=hunter2 eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.x")
	f.Add("-----BEGIN PRIVATE KEY-----")
	f.Fuzz(func(t *testing.T, s string) {
		out, n := Text(s)
		if n < 0 || (n == 0 && out != s) {
			t.Fatalf("count %d but output changed", n)
		}
		YAML(s)
	})
}
