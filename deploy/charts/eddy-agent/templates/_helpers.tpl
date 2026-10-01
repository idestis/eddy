{{- define "eddy-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "eddy-agent.fullname" -}}
{{- default .Chart.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "eddy-agent.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "eddy-agent.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "eddy-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "eddy-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "eddy-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "eddy-agent.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "eddy-agent.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- define "eddy-agent.tokenSecretName" -}}
{{- default (printf "%s-token" (include "eddy-agent.fullname" .)) .Values.token.existingSecret -}}
{{- end -}}

{{/* Only validate what is set: empty required values are reported by the agent and NOTES.txt. */}}
{{- define "eddy-agent.validate" -}}
{{- $url := .Values.hub.url -}}
{{- if and $url (not (hasPrefix "wss://" $url)) (not (and .Values.hub.allowInsecure (hasPrefix "ws://" $url))) -}}
{{- fail (printf "hub.url must start with wss:// (ws:// needs hub.allowInsecure=true, development only), got %q" $url) -}}
{{- end -}}
{{- range .Values.impersonation.allowedGroupPrefixes -}}
{{- if hasPrefix "system:" . -}}
{{- fail "impersonation.allowedGroupPrefixes must not include system:" -}}
{{- end -}}
{{- end -}}
{{- range .Values.impersonation.groups -}}
{{- if hasPrefix "system:" . -}}
{{- fail (printf "impersonation.groups must not include system: groups, got %q" .) -}}
{{- end -}}
{{- end -}}
{{- if and .Values.token.existingSecret .Values.token.value -}}
{{- fail "set either token.existingSecret or token.value, not both" -}}
{{- end -}}
{{- if and .Values.joinToken (or .Values.token.existingSecret .Values.token.value) -}}
{{- fail "joinToken replaces token.existingSecret and token.value: set only one of them" -}}
{{- end -}}
{{- if .Values.userRBAC.create -}}
{{- include "eddy-agent.validateUserRBAC" . -}}
{{- end -}}
{{- end -}}

{{- define "eddy-agent.userRBAC.labels" -}}
{{ include "eddy-agent.labels" . }}
app.kubernetes.io/part-of: eddy
app.kubernetes.io/component: user-rbac
{{- end -}}

{{/*
The rules of the user roles: get/list/watch on every kind Eddy watches (preset kinds only when
watch.presets enables them, as in the agent ClusterRole) and pods/log; with operator=true, also
patch on the Flux kinds. Never Secrets or ConfigMaps.
*/}}
{{- define "eddy-agent.userRBAC.rules" -}}
{{- $flux := ternary "[get, list, watch, patch]" "[get, list, watch]" .operator -}}
{{- $presets := .root.Values.watch.presets -}}
- apiGroups: [kustomize.toolkit.fluxcd.io]
  resources: [kustomizations]
  verbs: {{ $flux }}
- apiGroups: [helm.toolkit.fluxcd.io]
  resources: [helmreleases]
  verbs: {{ $flux }}
- apiGroups: [source.toolkit.fluxcd.io]
  resources: [gitrepositories, ocirepositories, helmrepositories, helmcharts, buckets]
  verbs: {{ $flux }}
- apiGroups: [apps]
  resources: [deployments, statefulsets, daemonsets, replicasets]
  verbs: [get, list, watch]
- apiGroups: [""]
  resources: [pods, events, namespaces, services, persistentvolumeclaims, serviceaccounts]
  verbs: [get, list, watch]
- apiGroups: [batch]
  resources: [jobs, cronjobs]
  verbs: [get, list, watch]
- apiGroups: [autoscaling]
  resources: [horizontalpodautoscalers]
  verbs: [get, list, watch]
- apiGroups: [networking.k8s.io]
  resources: [ingresses, networkpolicies]
  verbs: [get, list, watch]
- apiGroups: [policy]
  resources: [poddisruptionbudgets]
  verbs: [get, list, watch]
- apiGroups: [storage.k8s.io]
  resources: [storageclasses]
  verbs: [get, list, watch]
{{- if has "karpenter" $presets }}
# watch.presets: karpenter
- apiGroups: [karpenter.sh]
  resources: [nodepools, nodeclaims]
  verbs: [get, list, watch]
- apiGroups: [karpenter.k8s.aws]
  resources: [ec2nodeclasses]
  verbs: [get, list, watch]
{{- end }}
{{- if has "externalSecrets" $presets }}
# watch.presets: externalSecrets. The Secrets these produce stay unreadable.
- apiGroups: [external-secrets.io]
  resources: [externalsecrets, clusterexternalsecrets, secretstores, clustersecretstores, pushsecrets]
  verbs: [get, list, watch]
{{- end }}
# Logs are readable in the UI only with this (and over MCP only when mcp.allowLogs is on).
- apiGroups: [""]
  resources: [pods/log]
  verbs: [get]
{{- end -}}

{{/*
One binding of a user role: a ClusterRoleBinding, or with namespaces a RoleBinding (to the
ClusterRole) in each of them. Nothing is rendered without subjects. Subjects are only Groups and
Users; validate.yaml has already checked them.
*/}}
{{- define "eddy-agent.userRBAC.binding" -}}
{{- if or .groups .users }}
{{- $kinds := list (dict "kind" "ClusterRoleBinding" "namespace" "") }}
{{- if .namespaces }}
{{- $kinds = list }}
{{- range .namespaces }}
{{- $kinds = append $kinds (dict "kind" "RoleBinding" "namespace" .) }}
{{- end }}
{{- end }}
{{- range $kinds }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: {{ .kind }}
metadata:
  name: {{ $.name }}
  {{- with .namespace }}
  namespace: {{ . }}
  {{- end }}
  labels:
    {{- include "eddy-agent.userRBAC.labels" $.root | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ $.role }}
subjects:
  {{- range $.groups }}
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: {{ . | quote }}
  {{- end }}
  {{- range $.users }}
  - apiGroup: rbac.authorization.k8s.io
    kind: User
    name: {{ . | quote }}
  {{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{/* Subjects of every user binding, as {where, groups, users}. */}}
{{- define "eddy-agent.userRBAC.subjectSets" -}}
{{- $u := .Values.userRBAC -}}
{{- $sets := list (dict "where" "userRBAC.viewer" "groups" ($u.viewer.groups | default (list)) "users" ($u.viewer.users | default (list))) (dict "where" "userRBAC.operator" "groups" ($u.operator.groups | default (list)) "users" ($u.operator.users | default (list))) -}}
{{- range $i, $b := $u.bindings -}}
{{- $sets = append $sets (dict "where" (printf "userRBAC.bindings[%d] (%s)" $i $b.name) "groups" ($b.groups | default (list)) "users" ($b.users | default (list))) -}}
{{- end -}}
{{- toJson (dict "sets" $sets) -}}
{{- end -}}

{{- define "eddy-agent.validateUserRBAC" -}}
{{- $u := .Values.userRBAC -}}
{{- $prefix := $u.groupPrefix -}}
{{- if or (not (hasSuffix ":" $prefix)) (hasPrefix "system:" $prefix) -}}
{{- fail (printf "userRBAC.groupPrefix must end with ':' and must not be system:, got %q" $prefix) -}}
{{- end -}}
{{- $allowed := false -}}
{{- range .Values.impersonation.allowedGroupPrefixes -}}
{{- if hasPrefix . $prefix }}{{ $allowed = true }}{{ end -}}
{{- end -}}
{{- if not $allowed -}}
{{- fail (printf "userRBAC.groupPrefix %q must start with one of impersonation.allowedGroupPrefixes, or the agent could never impersonate those groups" $prefix) -}}
{{- end -}}
{{- if eq $u.roleNames.viewer $u.roleNames.operator -}}
{{- fail "userRBAC.roleNames.viewer and userRBAC.roleNames.operator must differ" -}}
{{- end -}}
{{- $agentSA := printf "system:serviceaccount:%s:%s" .Release.Namespace (include "eddy-agent.serviceAccountName" .) -}}
{{- range (include "eddy-agent.userRBAC.subjectSets" . | fromJson).sets -}}
{{- $where := .where -}}
{{- range .groups -}}
{{- if hasPrefix "system:" . -}}
{{- fail (printf "%s.groups must not include system: groups, got %q" $where .) -}}
{{- end -}}
{{- if not (hasPrefix $prefix .) -}}
{{- fail (printf "%s.groups: %q must start with userRBAC.groupPrefix %q (the hub's auth.groups.prefix); other groups are never impersonated" $where . $prefix) -}}
{{- end -}}
{{- end -}}
{{- range .users -}}
{{- $user := . -}}
{{- if eq $user $agentSA -}}
{{- fail (printf "%s.users must not include the agent's own ServiceAccount %q" $where $user) -}}
{{- end -}}
{{- if hasPrefix "system:" $user -}}
{{- fail (printf "%s.users must not include system: users, got %q" $where $user) -}}
{{- end -}}
{{- range $.Values.impersonation.denyUserPrefixes -}}
{{- if hasPrefix . $user -}}
{{- fail (printf "%s.users: %q matches impersonation.denyUserPrefixes %q, so the agent would never impersonate it" $where $user .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $seen := dict -}}
{{- range $u.bindings -}}
{{- $key := printf "%s/%s" .role .name -}}
{{- if hasKey $seen $key -}}
{{- fail (printf "userRBAC.bindings: duplicate binding %q for role %s" .name .role) -}}
{{- end -}}
{{- $_ := set $seen $key true -}}
{{- end -}}
{{- end -}}
