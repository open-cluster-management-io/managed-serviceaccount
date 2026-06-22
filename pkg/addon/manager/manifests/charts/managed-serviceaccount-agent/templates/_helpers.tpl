{{- define "managed-serviceaccount-agent.externalManagedKubeConfigNamespace" -}}
{{- default .Values.clusterName .Values.ExternalManagedKubeConfigNamespace -}}
{{- end -}}
