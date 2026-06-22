{{/*
Image tag shared across managed-serviceaccount images, defaulting to the chart version.
*/}}
{{- define "managed-serviceaccount-common.imageTag" -}}
{{- .Values.tag | default (print "v" .Chart.Version) -}}
{{- end -}}

{{/*
Return the managed-serviceaccount image.
*/}}
{{- define "managed-serviceaccount-common.image" -}}
{{- printf "%s:%s" .Values.image (include "managed-serviceaccount-common.imageTag" .) -}}
{{- end -}}

{{/*
Return the addon agent image, preserving the addon-framework Image override.
*/}}
{{- define "managed-serviceaccount-common.agentImage" -}}
{{- .Values.Image | default (include "managed-serviceaccount-common.image" .) -}}
{{- end -}}
