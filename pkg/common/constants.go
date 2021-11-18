package common

const (
	AddonName                  = "managed-serviceaccount"
	AgentName                  = "addon-agent"
	AddonAgentInstallNamespace = "open-cluster-management-" + AddonName

	HubAddonUserGroup = "system:open-cluster-management:addon:managed-serviceaccount"
)

const (
	LabelKeyIsManagedServiceAccount        = "authentication.open-cluster-management.io/is-managed-serviceaccount"
	LabelKeyManagedServiceAccountNamespace = "authentication.open-cluster-management.io/managed-serviceaccount-namespace"
	LabelKeyManagedServiceAccountName      = "authentication.open-cluster-management.io/managed-serviceaccount-name"
)
