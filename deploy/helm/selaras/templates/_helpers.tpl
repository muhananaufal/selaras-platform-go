{{/*
Label bersama. app.kubernetes.io/name adalah nama unit, supaya satu
kubectl get pods -l app.kubernetes.io/name=identity-svc cukup.
*/}}
{{- define "selaras.labels" -}}
app.kubernetes.io/name: {{ .unit }}
app.kubernetes.io/part-of: selaras
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/version: {{ .root.Values.image.tag | quote }}
{{- end }}

{{- define "selaras.selector" -}}
app.kubernetes.io/name: {{ .unit }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{/*
Nama image: <registry>/<repository>/<unit>:<tag>, tanpa garis miring ganda
saat registry kosong (image lokal yang di-import ke k3d).
*/}}
{{- define "selaras.image" -}}
{{- $v := .root.Values.image -}}
{{- if $v.registry -}}
{{ $v.registry }}/{{ $v.repository }}/{{ .unit }}:{{ $v.tag }}
{{- else -}}
{{ $v.repository }}/{{ .unit }}:{{ $v.tag }}
{{- end -}}
{{- end }}
