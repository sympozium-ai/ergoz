{{- define "ergoz.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: ergoz
{{- end }}

{{/* Released images are tagged vX.Y.Z; appVersion is stored bare. */}}
{{- define "ergoz.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- end }}

{{- define "ergoz.securityContext" -}}
runAsNonRoot: true
readOnlyRootFilesystem: true
allowPrivilegeEscalation: false
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* Agent variant for nvidia.devAccess: privileged lifts the device-cgroup
     deny so NVML can open /dev/nvidia* (world-rw, so non-root still holds).
     allowPrivilegeEscalation must be true — the API rejects privileged
     containers that set it false. */}}
{{- define "ergoz.securityContextDevAccess" -}}
privileged: true
allowPrivilegeEscalation: true
runAsNonRoot: true
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end }}
