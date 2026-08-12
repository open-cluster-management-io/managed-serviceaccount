{{- define "managed-serviceaccount-agent.externalManagedKubeConfigNamespace" -}}
{{- default .Values.clusterName .Values.externalManagedKubeConfigNamespace -}}
{{- end -}}

{{- define "managed-serviceaccount-agent.provisionerName" -}}
managed-serviceaccount-kubeconfig-provisioner
{{- end -}}

{{- define "managed-serviceaccount-agent.provisionerSourceRBACName" -}}
{{- printf "%s-source-%s" (include "managed-serviceaccount-agent.provisionerName" .) .Values.addonInstallNamespace -}}
{{- end -}}

{{- define "managed-serviceaccount-agent.hostingAgentServiceAccountName" -}}
managed-serviceaccount
{{- end -}}
