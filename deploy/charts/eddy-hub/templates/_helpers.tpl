{{- define "eddy-hub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fixed default name (eddy-hub) so Service names are stable: eddy-hub and eddy-hub-agents. */}}
{{- define "eddy-hub.fullname" -}}
{{- default .Chart.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "eddy-hub.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "eddy-hub.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "eddy-hub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "eddy-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "eddy-hub.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "eddy-hub.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "eddy-hub.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* Port number from a listen address such as ":8080" or "127.0.0.1:8080". */}}
{{- define "eddy-hub.port" -}}
{{- regexFind "[0-9]+$" . -}}
{{- end -}}

{{- define "eddy-hub.usersSecretName" -}}
{{- default "eddy-users" .Values.users.existingSecret -}}
{{- end -}}

{{- define "eddy-hub.keySecretName" -}}
{{- default (printf "%s-key" (include "eddy-hub.fullname" .)) .Values.sessionKeySecret -}}
{{- end -}}

{{- define "eddy-hub.ephemeral" -}}
{{- if eq .Values.store.driver "memory" -}}true{{- end -}}
{{- end -}}

{{/* Replicas: the memory store lives inside one pod, so it always runs one. */}}
{{- define "eddy-hub.replicas" -}}
{{- if eq .Values.store.driver "memory" -}}1{{- else -}}{{ int .Values.replicaCount }}{{- end -}}
{{- end -}}

{{/* DNS name of the headless peer Service. */}}
{{- define "eddy-hub.peerService" -}}
{{- printf "%s-peers.%s.svc" (include "eddy-hub.fullname" .) .Release.Namespace -}}
{{- end -}}

{{/*
Base URL agents dial (https://host), for hub.yaml's agentsPublicURL. Empty when neither
agentsPublicURL nor an ingress.agents host is set: the in-cluster Service address only works
for an agent in the hub's own cluster, so the install guide warns instead of printing it.
*/}}
{{- define "eddy-hub.agentsBaseURL" -}}
{{- if .Values.agentsPublicURL -}}
{{- trimSuffix "/" .Values.agentsPublicURL -}}
{{- else if .Values.ingress.agents.hosts -}}
{{- printf "https://%s" (first .Values.ingress.agents.hosts) -}}
{{- end -}}
{{- end -}}

{{/* Validation: fail early with a clear message. */}}
{{- define "eddy-hub.validate" -}}
{{- $prefix := .Values.config.auth.groups.prefix -}}
{{- range .Values.onboarding.admins.groups -}}
{{- if hasPrefix "system:" . -}}
{{- fail (printf "onboarding.admins.groups: %q is a system group; bind only Eddy groups" .) -}}
{{- end -}}
{{- if not (hasPrefix $prefix .) -}}
{{- fail (printf "onboarding.admins.groups: %q must start with config.auth.groups.prefix %q, the prefix the hub gives every group" . $prefix) -}}
{{- end -}}
{{- end -}}
{{- range .Values.onboarding.admins.users -}}
{{- if hasPrefix "system:" . -}}
{{- fail (printf "onboarding.admins.users: %q is a system user" .) -}}
{{- end -}}
{{- end -}}
{{- if not (has .Values.store.driver (list "postgres" "memory")) -}}
{{- fail (printf "store.driver %q is not supported (postgres, memory). SQLite was removed in favour of PostgreSQL (ADR-0004)." .Values.store.driver) -}}
{{- end -}}
{{- if and (eq .Values.store.driver "postgres") (not .Values.store.postgres.dsnSecret.name) -}}
{{- fail "store.postgres.dsnSecret.name is required: a Secret whose key (store.postgres.dsnSecret.key, default dsn) holds the PostgreSQL DSN. With CloudNativePG use {name: <cluster>-app, key: uri}. store.driver=memory is for evaluation only." -}}
{{- end -}}
{{- if and .Values.oauth2Proxy.enabled (not .Values.credentialsSecret) (not .Values.config.auth.proxy.insecureSkipSharedSecret) -}}
{{- fail "oauth2Proxy.enabled needs credentialsSecret with the proxy shared secret (EDDY_PROXY_SECRET, >= 32 random bytes), so the hub trusts only your proxy." -}}
{{- end -}}
{{- if and .Values.oauth2Proxy.enabled (not .Values.oauth2Proxy.existingSecret) -}}
{{- fail "oauth2Proxy.enabled needs oauth2Proxy.existingSecret (OAUTH2_PROXY_COOKIE_SECRET and your provider credentials)." -}}
{{- end -}}
{{- if and .Values.config.auth.proxy.enabled (not .Values.oauth2Proxy.enabled) (not .Values.config.auth.proxy.trustedCIDRs) -}}
{{- fail "config.auth.proxy.enabled needs config.auth.proxy.trustedCIDRs (the TCP peers of your proxy)." -}}
{{- end -}}
{{- $a := .Values.config.auth -}}
{{- $gh := default dict $a.github -}}
{{- $oidc := default list $a.oidc -}}
{{- if $gh.enabled -}}
{{- if not $gh.clientID -}}
{{- fail "config.auth.github.enabled needs config.auth.github.clientID (from your GitHub App or OAuth App)." -}}
{{- end -}}
{{- if and (not $gh.allowedOrganizations) (not $gh.allowAllUsers) -}}
{{- fail "config.auth.github needs allowedOrganizations: without it any GitHub account could sign in (set allowAllUsers only for GitHub Enterprise Server)." -}}
{{- end -}}
{{- if not .Values.credentialsSecret -}}
{{- fail (printf "config.auth.github needs credentialsSecret with the client secret under %s." (default "GITHUB_CLIENT_SECRET" $gh.clientSecretEnv)) -}}
{{- end -}}
{{- end -}}
{{- if and $oidc (not .Values.credentialsSecret) -}}
{{- fail "config.auth.oidc needs credentialsSecret with each provider's client secret (OIDC_<ID>_CLIENT_SECRET)." -}}
{{- end -}}
{{- range $i, $o := $oidc -}}
{{- if and (not $o.issuer) (ne (toString $o.preset) "google") -}}
{{- fail (printf "config.auth.oidc[%d] (%s) needs issuer (only the google preset has a default)." $i (toString $o.id)) -}}
{{- end -}}
{{- if and (eq (toString $o.preset) "google") (not $o.allowedDomains) (not $o.allowAllUsers) -}}
{{- fail (printf "config.auth.oidc[%d] (%s): the google preset needs allowedDomains, or any Google account could sign in." $i (toString $o.id)) -}}
{{- end -}}
{{- end -}}
{{- if and $a.local.enabled (eq (toString $a.local.mode) "breakglass") (not (or $gh.enabled $oidc $a.proxy.enabled .Values.oauth2Proxy.enabled)) -}}
{{- fail "config.auth.local.mode=breakglass hides the password form, so enable config.auth.github, config.auth.oidc or proxy auth too." -}}
{{- end -}}
{{- range $i, $c := .Values.clusters -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" (toString $c.name)) -}}
{{- fail (printf "clusters[%d].name %q must be a DNS-1123 label" $i (toString $c.name)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
hub.yaml as YAML. User config first, then the values the chart owns.
*/}}
{{- define "eddy-hub.hubConfig" -}}
{{- $sidecar := .Values.oauth2Proxy.enabled -}}
{{- $listen := dict -}}
{{- if $sidecar -}}
{{- $_ := set $listen "ui" (printf "127.0.0.1:%s" (include "eddy-hub.port" .Values.config.listen.ui)) -}}
{{- end -}}
{{- if .Values.agentTLS.secretName -}}
{{- $_ := set $listen "agentTLS" (dict "certFile" "/etc/eddy/agent-tls/tls.crt" "keyFile" "/etc/eddy/agent-tls/tls.key") -}}
{{- end -}}
{{- $auth := dict "keyFile" "/etc/eddy/key/key" "local" (dict "usersFile" "/etc/eddy/users/users.yaml") -}}
{{- if $sidecar -}}
{{- $_ := set $auth "proxy" (dict "enabled" true "trustedCIDRs" (default (list "127.0.0.1/32") .Values.config.auth.proxy.trustedCIDRs)) -}}
{{- end -}}
{{- $store := dict "driver" .Values.store.driver "retention" .Values.store.retention -}}
{{- if eq .Values.store.driver "postgres" -}}
{{- $_ := set $store "postgres" (dict "dsnEnv" "EDDY_DATABASE_URL" "maxOpenConns" (int .Values.store.postgres.maxOpenConns)) -}}
{{- end -}}
{{- $owned := dict "publicURL" .Values.publicURL "namespace" .Release.Namespace "listen" $listen "auth" $auth "store" $store "runtime" (dict "flagsFile" "/etc/eddy/runtime/flags.yaml") -}}
{{- if eq .Values.store.driver "postgres" -}}
{{/* podName and advertise default to $POD_NAME and $POD_IP (set in the Deployment). */}}
{{- $_ := set $owned "peer" (dict "listen" (printf ":%d" (int .Values.peer.port)) "service" (include "eddy-hub.peerService" .)) -}}
{{- end -}}
{{- with include "eddy-hub.agentsBaseURL" . -}}
{{- $_ := set $owned "agentsPublicURL" . -}}
{{- end -}}
{{- $_ := set $owned "onboarding" (dict "enabled" (and .Values.onboarding.enabled .Values.rbac.create) "joinTokenTTL" .Values.onboarding.joinTokenTTL "agentChartVersion" .Chart.Version "agentImage" (printf "ghcr.io/idestis/eddy-agent:%s" .Chart.AppVersion)) -}}
{{- $cfg := mustMergeOverwrite (deepCopy .Values.config) $owned -}}
{{- toYaml $cfg -}}
{{- end -}}

{{/* oauth2-proxy alpha config: chart-owned keys win over alphaConfig. */}}
{{- define "eddy-hub.oauth2ProxyConfig" -}}
{{- $p := .Values.config.auth.proxy -}}
{{- $headers := list
  (dict "name" $p.userHeader "values" (list (dict "claimSource" (dict "claim" "email"))))
  (dict "name" $p.groupsHeader "values" (list (dict "claimSource" (dict "claim" "groups"))))
  (dict "name" $p.sharedSecretHeader "values" (list (dict "secretSource" (dict "fromFile" (printf "/etc/oauth2-proxy/secret/%s" $p.sharedSecretEnv))))) -}}
{{- $owned := dict
  "server" (dict "bindAddress" "0.0.0.0:4180")
  "upstreamConfig" (dict "upstreams" (list (dict "id" "eddy" "path" "/" "flushInterval" "100ms" "timeout" "1h" "uri" (printf "http://127.0.0.1:%s" (include "eddy-hub.port" .Values.config.listen.ui)))))
  "injectRequestHeaders" $headers -}}
{{- toYaml (mustMergeOverwrite (deepCopy .Values.oauth2Proxy.alphaConfig) $owned) -}}
{{- end -}}
