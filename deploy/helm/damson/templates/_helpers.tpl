{{/* Names and labels. */}}
{{- define "damson.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "damson.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "damson.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "damson.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "damson.controllerSelector" -}}
app.kubernetes.io/name: {{ include "damson.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end -}}

{{- define "damson.agentSelector" -}}
app.kubernetes.io/name: {{ include "damson.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: agent
damson.dev/pool: {{ .pool.name | quote }}
{{- end -}}

{{/* The labels an agent pod adds to damson.labels. */}}
{{- define "damson.agentLabels" -}}
app.kubernetes.io/component: agent
damson.dev/pool: {{ .pool.name | quote }}
{{- end -}}

{{- define "damson.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "damson.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "damson.image" -}}
{{- printf "%s:%s" .repository (default .defaultTag .tag) -}}
{{- end -}}

{{/* The URL agents dial. */}}
{{- define "damson.controllerUrl" -}}
{{- if .Values.agents.controllerUrl -}}
{{- .Values.agents.controllerUrl -}}
{{- else if and .Values.ingress.enabled .Values.ingress.hosts -}}
{{- printf "wss://%s" (index .Values.ingress.hosts 0).host -}}
{{- else -}}
{{- fail "agents.controllerUrl is required (wss://host) unless the ingress is enabled" -}}
{{- end -}}
{{- end -}}

{{/* A secret key reference. */}}
{{- define "damson.secretEnv" -}}
- name: {{ .name }}
  valueFrom:
    secretKeyRef:
      name: {{ .secret }}
      key: {{ .key }}
{{- end -}}

{{/* Pod security defaults, restricted profile. */}}
{{- define "damson.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "damson.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{/* The agent images run as uid 10001 (deploy/docker/Dockerfile). */}}
{{- define "damson.agentPodSecurityContext" -}}
runAsNonRoot: true
runAsUser: 10001
runAsGroup: 10001
fsGroup: 10001
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "damson.agentServiceAccountName" -}}
{{- if .Values.agents.serviceAccount.create -}}
{{- default (printf "%s-agent" (include "damson.fullname" .)) .Values.agents.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.agents.serviceAccount.name -}}
{{- end -}}
{{- end -}}
