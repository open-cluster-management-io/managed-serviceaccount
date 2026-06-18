{{/*
Render the hub manager when this chart needs a hub-side manager process.
*/}}
{{- define "managed-serviceaccount.hubManager.enabled" -}}
{{- if or (ne .Values.hubDeployMode "AddOnTemplate") ((.Values.featureGates | default dict).clusterProfile) -}}
true
{{- end -}}
{{- end -}}
