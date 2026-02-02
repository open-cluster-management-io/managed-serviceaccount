# Cluster Profile Credentials Plugin

This plugin provides cluster credentials to access spoke clusters via the ClusterProfile credential sync mechanism.

When executed by a Kubernetes client authentication flow, this plugin:
1. Extracts the `clusterName` from the ExecCredential cluster config
2. Retrieves the synced token secret from `<clusterName>-<MANAGED_SERVICEACCOUNT_NAME>`
3. Returns the service account token as an ExecCredential response

The secret is synced by the ClusterProfileCredSyncer controller from ManagedServiceAccount token secrets in spoke cluster namespaces.

## Build

```bash
# Build a statically linked plugin binary to run in any environment.
CGO_ENABLED=0 GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) \
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
        "provideClusterInfo": true,
        "interactiveMode": "Never"
      }
    }
  ]
}
```

Note: Ensure the clusterprofile credentials plugin has sufficient permissions to list secrets in the controller’s running namespace.

## How it Works

The plugin integrates with the ClusterProfile credential sync flow:

1. **Controller Syncs Credentials**: The ClusterProfileCredSyncer controller watches ManagedServiceAccounts and syncs their token secrets to the ClusterProfile namespace
   - ManagedServiceAccount in namespace `cluster1` → ClusterProfile named `cluster1`
   - Secret naming: `<clusterName>-<MANAGED_SERVICEACCOUNT_NAME>` (e.g., `cluster1-admin`)

2. **Plugin Retrieves Token**: When a client needs to authenticate to a spoke cluster:
   - The client exec flow calls this plugin with cluster information
   - Plugin extracts `clusterName` from the exec config
   - Plugin reads the synced secret: `<clusterName>-<MANAGED_SERVICEACCOUNT_NAME>`
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
