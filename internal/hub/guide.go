package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/version"
)

// Install guide defaults. They mirror the eddy-agent chart's defaults, so
// the plain manifests install the same thing as the Helm command.
const (
	guideRelease      = "eddy-agent"
	guideReplicas     = 2
	guideTokenKey     = "token"
	guideJoinKey      = "joinToken"
	guideHubURLHolder = "wss://<agents endpoint>/agent/v1/connect"
	guideTokenHolder  = "<join token>"
	guideNetworkDocs  = "https://github.com/idestis/eddy/blob/main/docs/agent.md#how-it-connects-outbound-only"
)

// installGuide is the "connect this cluster" guide in three forms, plus the
// Cluster resource for GitOps-managed hubs.
type installGuide struct {
	HubURL          string   `json:"hubURL"`
	Namespace       string   `json:"namespace"`
	Helm            string   `json:"helm"`
	Values          string   `json:"values"`
	Manifests       string   `json:"manifests"`
	ClusterResource string   `json:"clusterResource"`
	NetworkDocs     string   `json:"networkDocs"`
	Warnings        []string `json:"warnings,omitempty"`
}

// agentsURL turns agentsPublicURL into the agent endpoint, or "" when it is
// not configured.
func agentsURL(base string) string {
	if base == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSuffix(base, "/"))
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	if !strings.HasSuffix(u.Path, "/agent/v1/connect") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/agent/v1/connect"
	}
	return u.String()
}

type guideData struct {
	Cluster      ClusterSpec
	HubURL       string
	Token        string
	Expires      string
	Namespace    string
	Chart        string
	ChartVersion string
	Image        string
	Release      string
	SecretName   string
	TokenKey     string
	JoinKey      string
	Replicas     int
	QPS, Burst   int
	Concurrency  int
	LogStreams   int
	SAR          int
	Insecure     bool
}

// q quotes a value for YAML and shell (a JSON string is both, for the
// characters that can appear here).
func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var guideTemplates = template.Must(template.New("guide").Funcs(template.FuncMap{"q": q}).Parse(`
{{- define "helm" -}}
helm upgrade --install {{ .Release }} {{ .Chart }} --version {{ .ChartVersion }} \
  --namespace {{ .Namespace }} --create-namespace \
  --set cluster.name={{ .Cluster.Name }} \
  --set hub.url={{ .HubURL }} \
{{- if .Insecure }}
  --set hub.allowInsecure=true \
{{- end }}
  --set-string joinToken={{ .Token }}
{{- end -}}

{{- define "values" -}}
# eddy-agent values for cluster {{ .Cluster.Name }}.
# helm upgrade --install {{ .Release }} {{ .Chart }} --version {{ .ChartVersion }} -n {{ .Namespace }} --create-namespace -f values.yaml
cluster:
  name: {{ .Cluster.Name }}
hub:
  url: {{ q .HubURL }}
{{- if .Insecure }}
  allowInsecure: true
{{- end }}
# One-time join token{{ if .Expires }}, valid until {{ .Expires }}{{ end }}. It is worthless once the agent
# has joined, so committing it is acceptable, but prefer a SOPS or External Secrets reference.
joinToken: {{ q .Token }}
# Recommended: pin group impersonation to the groups you use (see docs/agent.md).
# impersonation:
#   groups: ["eddy:authenticated", "eddy:platform"]
{{- end -}}

{{- define "manifests" -}}
# Plain manifests equivalent to the eddy-agent chart {{ .ChartVersion }} with its defaults,
# for cluster {{ .Cluster.Name }}. Apply them in the WORKLOAD cluster.
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Namespace }}
  labels:
    pod-security.kubernetes.io/enforce: restricted
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .Release }}
  namespace: {{ .Namespace }}
---
# Read-only cache access, SubjectAccessReviews and impersonation. No write verbs, and no
# access to Secrets or ConfigMaps.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ .Release }}
rules:
  - apiGroups: [kustomize.toolkit.fluxcd.io]
    resources: [kustomizations]
    verbs: [get, list, watch]
  - apiGroups: [helm.toolkit.fluxcd.io]
    resources: [helmreleases]
    verbs: [get, list, watch]
  - apiGroups: [source.toolkit.fluxcd.io]
    resources: [gitrepositories, ocirepositories, helmrepositories, helmcharts, buckets]
    verbs: [get, list, watch]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets, replicasets]
    verbs: [get, list, watch]
  - apiGroups: [""]
    resources: [pods, events, namespaces, services, persistentvolumeclaims]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs, cronjobs]
    verbs: [get, list, watch]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [get, list, watch]
  - apiGroups: [networking.k8s.io]
    resources: [ingresses]
    verbs: [get, list, watch]
  - apiGroups: [authorization.k8s.io]
    resources: [subjectaccessreviews]
    verbs: [create]
  - apiGroups: [""]
    resources: [users]
    verbs: [impersonate]
  - apiGroups: [""]
    resources: [groups]
    verbs: [impersonate]
    # Recommended: pin to the groups you use.
    # resourceNames: ["eddy:authenticated", "eddy:platform"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ .Release }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ .Release }}
subjects:
  - kind: ServiceAccount
    name: {{ .Release }}
    namespace: {{ .Namespace }}
---
# The agent's token Secret. It starts with the one-time join token; the agent stores its
# permanent token under "{{ .TokenKey }}" after joining.
apiVersion: v1
kind: Secret
metadata:
  name: {{ .SecretName }}
  namespace: {{ .Namespace }}
type: Opaque
stringData:
  {{ .JoinKey }}: {{ q .Token }}
---
# The agent's only write permission: get and update on its own token Secret.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .SecretName }}
  namespace: {{ .Namespace }}
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [{{ .SecretName }}]
    verbs: [get, update]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .SecretName }}
  namespace: {{ .Namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .SecretName }}
subjects:
  - kind: ServiceAccount
    name: {{ .Release }}
    namespace: {{ .Namespace }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release }}
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/name: eddy-agent
    app.kubernetes.io/instance: {{ .Release }}
spec:
  replicas: {{ .Replicas }}
  strategy:
    type: RollingUpdate
    rollingUpdate: {maxSurge: 1, maxUnavailable: 0}
  selector:
    matchLabels:
      app.kubernetes.io/name: eddy-agent
      app.kubernetes.io/instance: {{ .Release }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: eddy-agent
        app.kubernetes.io/instance: {{ .Release }}
    spec:
      serviceAccountName: {{ .Release }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile: {type: RuntimeDefault}
      containers:
        - name: agent
          image: {{ .Image }}
          env:
            - {name: EDDY_CLUSTER, value: {{ q .Cluster.Name }}}
            - {name: EDDY_HUB_URL, value: {{ q .HubURL }}}
            - name: EDDY_AGENT_TOKEN
              valueFrom: {secretKeyRef: {name: {{ .SecretName }}, key: {{ .TokenKey }}, optional: true}}
            - name: EDDY_JOIN_TOKEN
              valueFrom: {secretKeyRef: {name: {{ .SecretName }}, key: {{ .JoinKey }}, optional: true}}
            - {name: EDDY_TOKEN_SECRET, value: {{ .SecretName }}}
            - {name: EDDY_TOKEN_SECRET_KEY, value: {{ .TokenKey }}}
            - name: POD_NAMESPACE
              valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
            - {name: EDDY_ALLOWED_GROUP_PREFIXES, value: "eddy:"}
            - {name: EDDY_DENY_USER_PREFIXES, value: "system:,eks:,kubernetes-admin"}
{{- if .Insecure }}
            - {name: EDDY_ALLOW_INSECURE, value: "1"}
{{- end }}
            - {name: EDDY_HEALTH_ADDR, value: ":8081"}
            # The chart's limits.* divided by {{ .Replicas }} replicas.
            - {name: EDDY_KUBE_QPS, value: "{{ .QPS }}"}
            - {name: EDDY_KUBE_BURST, value: "{{ .Burst }}"}
            - {name: EDDY_MAX_CONCURRENT, value: "{{ .Concurrency }}"}
            - {name: EDDY_MAX_LOG_STREAMS, value: "{{ .LogStreams }}"}
            - {name: EDDY_MAX_SAR_CONCURRENT, value: "{{ .SAR }}"}
          ports:
            - {name: health, containerPort: 8081}
          livenessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 3
            periodSeconds: 5
          resources:
            requests: {cpu: 50m, memory: 128Mi}
            limits: {memory: 512Mi}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            capabilities: {drop: [ALL]}
            seccompProfile: {type: RuntimeDefault}
          volumeMounts:
            - {name: tmp, mountPath: /tmp}
      volumes:
        - name: tmp
          emptyDir: {medium: Memory, sizeLimit: 16Mi}
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                topologyKey: kubernetes.io/hostname
                labelSelector:
                  matchLabels:
                    app.kubernetes.io/name: eddy-agent
                    app.kubernetes.io/instance: {{ .Release }}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ .Release }}
  namespace: {{ .Namespace }}
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: eddy-agent
      app.kubernetes.io/instance: {{ .Release }}
---
# Egress only: DNS, the Kubernetes API and the hub. No inbound traffic.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ .Release }}
  namespace: {{ .Namespace }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: eddy-agent
      app.kubernetes.io/instance: {{ .Release }}
  policyTypes: [Ingress, Egress]
  ingress: []
  egress:
    - ports:
        - {port: 53, protocol: UDP}
        - {port: 53, protocol: TCP}
    - ports:
        - {port: 443, protocol: TCP}
        - {port: 6443, protocol: TCP}
{{- end -}}

{{- define "cluster" -}}
# The Cluster resource in the MANAGEMENT cluster, for hubs whose clusters live in Git.
apiVersion: gitops.eddy.dev/v1alpha1
kind: Cluster
metadata:
  name: {{ .Cluster.Name }}
spec:
  displayName: {{ q .Cluster.DisplayName }}
{{- if .Cluster.Environment }}
  environment: {{ q .Cluster.Environment }}
{{- end }}
{{- if .Cluster.Region }}
  region: {{ q .Cluster.Region }}
{{- end }}
{{- if .Cluster.Color }}
  color: {{ q .Cluster.Color }}
{{- end }}
  protected: {{ .Cluster.Protected }}
  order: {{ .Cluster.Order }}
  agentTokenSecretRef:
    name: eddy-agent-{{ .Cluster.Name }}
    key: token
{{- end -}}
`))

// renderGuide renders the install guide for spec. token may be empty, in
// which case a placeholder is shown (the Connection panel of an existing
// cluster, before a new join token is issued).
func renderGuide(cfg *config.Hub, spec ClusterSpec, token string, expires time.Time) (installGuide, error) {
	ob := cfg.Onboarding
	chartVersion := ob.AgentChartVersion
	if chartVersion == "" {
		chartVersion = strings.TrimPrefix(version.Version, "v")
	}
	image := ob.AgentImage
	if image == "" {
		image = "ghcr.io/idestis/eddy-agent:" + chartVersion
	}
	d := guideData{
		Cluster: spec, HubURL: agentsURL(cfg.AgentsPublicURL), Token: token,
		Namespace: ob.AgentNamespace, Chart: ob.AgentChart, ChartVersion: chartVersion, Image: image,
		Release: guideRelease, SecretName: guideRelease + "-token", TokenKey: guideTokenKey, JoinKey: guideJoinKey,
		Replicas: guideReplicas, QPS: 20 / guideReplicas, Burst: 40 / guideReplicas, Concurrency: 16 / guideReplicas,
		LogStreams: 8 / guideReplicas, SAR: 8 / guideReplicas,
	}
	g := installGuide{Namespace: d.Namespace, NetworkDocs: guideNetworkDocs}
	if d.HubURL == "" {
		d.HubURL = guideHubURLHolder
		g.Warnings = append(g.Warnings, "agentsPublicURL is not set on the hub: replace the placeholder hub URL with your agent endpoint.")
	}
	d.Insecure = strings.HasPrefix(d.HubURL, "ws://")
	if d.Insecure {
		g.Warnings = append(g.Warnings, "The agent endpoint is plain ws://: use it for local development only.")
	}
	if d.Token == "" {
		d.Token = guideTokenHolder
	}
	if !expires.IsZero() {
		d.Expires = expires.UTC().Format(time.RFC3339)
	}
	g.HubURL = d.HubURL
	for _, part := range []struct {
		name string
		dst  *string
	}{{"helm", &g.Helm}, {"values", &g.Values}, {"manifests", &g.Manifests}, {"cluster", &g.ClusterResource}} {
		var b bytes.Buffer
		if err := guideTemplates.ExecuteTemplate(&b, part.name, d); err != nil {
			return installGuide{}, fmt.Errorf("hub: render install guide %s: %w", part.name, err)
		}
		*part.dst = strings.TrimSpace(b.String()) + "\n"
	}
	return g, nil
}
