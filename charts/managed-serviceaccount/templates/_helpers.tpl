{{/*
Render the hub manager when this chart needs a hub-side manager process.
*/}}
{{- define "managed-serviceaccount.hubManager.enabled" -}}
{{- if or (ne .Values.hubDeployMode "AddOnTemplate") ((.Values.featureGates | default dict).clusterProfile) -}}
true
{{- end -}}
{{- end -}}

{{- define "managed-serviceaccount.addOnDeploymentConfigRef" -}}
- group: addon.open-cluster-management.io
  resource: addondeploymentconfigs
  namespace: {{ .namespace }}
  name: managed-serviceaccount
{{- end -}}
