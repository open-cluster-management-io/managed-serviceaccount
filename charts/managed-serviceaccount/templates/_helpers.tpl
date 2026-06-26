{{- define "managed-serviceaccount.addOnDeploymentConfigRef" -}}
- group: addon.open-cluster-management.io
  resource: addondeploymentconfigs
  namespace: {{ .namespace }}
  name: managed-serviceaccount
{{- end -}}
