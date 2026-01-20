# Cluster Profile Credentials Plugin

This plugin provides cluster credentials for accessing spoke clusters via the ClusterProfile credential sync mechanism.

When executed by a Kubernetes client authentication flow, this plugin:
1. Extracts the `clusterName` from the ExecCredential cluster config
2. Retrieves the synced token secret from `<CLUSTER_PROFILE_NAMESPACE>/<clusterName>-<MANAGED_SERVICEACCOUNT_NAME>`
3. Returns the service account token as an ExecCredential response

The secret is synced by the ClusterProfileCredSyncer controller from ManagedServiceAccount token secrets in spoke cluster namespaces.

## Required RBAC

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: cp-creds-role
  namespace: <CLUSTER_PROFILE_NAMESPACE>
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cp-creds-rolebinding
  namespace: <CLUSTER_PROFILE_NAMESPACE>
subjects:
- kind: ServiceAccount
  name: <CONTROLLER_SERVICE_ACCOUNT_NAME>
  namespace: <CONTROLLER_SERVICE_ACCOUNT_NAMESPACE>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: cp-creds-role
```

## Build

```bash
go build -o ./bin/cp-creds ./cmd/clusterprofile-credentials-plugin
```

## Usage in a controller

Use the following provider config to exec the clusterprofile credentials plugin.

```jsonc
{
  "providers": [
    {
      "name": "open-cluster-management",
      "execConfig": {
        "apiVersion": "client.authentication.k8s.io/v1",
        "command": "./bin/cp-creds",
        "args": ["--managed-serviceaccount=<MANAGED_SERVICEACCOUNT_NAME>"],
        "provideClusterInfo": true
      }
    }
  ]
}
```

## How it Works

The plugin integrates with the ClusterProfile credential sync flow:

1. **Controller Syncs Credentials**: The ClusterProfileCredSyncer controller watches ManagedServiceAccounts and syncs their token secrets to the ClusterProfile namespace
   - ManagedServiceAccount in namespace `cluster1` → ClusterProfile named `cluster1` in namespace `open-cluster-management`
   - Secret naming: `<clusterName>-<msaName>` (e.g., `cluster1-admin`)

2. **Plugin Retrieves Token**: When a client needs to authenticate to a spoke cluster:
   - The client exec flow calls this plugin with cluster information
   - Plugin extracts `clusterName` from the exec config
   - Plugin reads the synced secret: `open-cluster-management/<clusterName>-<msaName>`
   - Plugin returns the service account token

### ClusterProfile Configuration

The `ClusterProfile.status.accessProviders[].cluster.extensions` field must include the cluster name:

- Set `extensions[].name` to `client.authentication.k8s.io/exec`
- Set `extensions[].extension.clusterName` to the spoke cluster name
- This `clusterName` is passed to the plugin via `ExecCredential.Spec.Cluster.Config`

Example ClusterProfile status:

```yaml
status:
  accessProviders:
  - name: open-cluster-management
    cluster:
      server: https://cluster-proxy-addon-user.open-cluster-management-addon:9092/cluster1
      certificate-authority-data: <BASE64_CA>
      extensions:
      - name: client.authentication.k8s.io/exec
        extension:
          clusterName: cluster1  # Must match the spoke cluster namespace and ClusterProfile name
```
