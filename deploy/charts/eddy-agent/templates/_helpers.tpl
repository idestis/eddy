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
{{- end -}}
