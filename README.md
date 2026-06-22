# Managed Service Account

## Overview

The Managed Service Account addon is an Open Cluster Management (OCM) addon built on the [addon-framework](https://github.com/open-cluster-management-io/addon-framework). It synchronizes [ServiceAccount](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/) resources to managed clusters and collects their authentication tokens back to the hub cluster as secret resources.

## Use Cases

This addon is beneficial when you need to:

- Deploy service account resources to managed clusters without requiring a kubeconfig to each managed cluster
- Access the Kubernetes API of managed clusters from the hub cluster using valid authentication tokens  
- Standardize client identity by using the same service account across managed cluster API requests

## Architecture

The addon follows the standard OCM [addon architecture](https://open-cluster-management.io/concepts/addon/) with two main components:

**Addon Manager**
- Automatically installs the addon agent into managed clusters
- Manages required resources and dependencies

**Addon Agent**  
- Monitors the ManagedServiceAccount API resources
- Periodically projects service account tokens as secret resources to the hub cluster
- Handles token refresh according to the configured rotation policy

## Installation

### Prerequisites

- Open Cluster Management (OCM) registration (version >= 0.5.0)

### Installation Steps

Install the addon using Helm charts:

```shell
# Add the OCM Helm repository
helm repo add ocm https://open-cluster-management.io/helm-charts/
helm repo update

# Search for the managed-serviceaccount chart
helm search repo ocm/managed-serviceaccount

# Install the addon
helm install \
    -n open-cluster-management-addon --create-namespace \
    managed-serviceaccount ocm/managed-serviceaccount
```

### Verification

Confirm the installation was successful:

```shell
kubectl get managedclusteraddon -A | grep managed-serviceaccount
```

Expected output:
```
NAMESPACE        NAME                     AVAILABLE   DEGRADED   PROGRESSING
<your-cluster>   managed-serviceaccount   True        False      False
```

## Usage

### Prometheus Metrics Scraping

Set `prometheus.enabled=true` to expose addon metrics with Prometheus
`ServiceMonitor` resources. For addon agent metrics, configure the
`ManagedClusterAddOn` with an `AddOnDeploymentConfig`; see
`deploy/samples/addon-agent-metrics-scraping.yaml` for an example.

The Prometheus Operator `ServiceMonitor` CRD must exist on the cluster where the
ServiceMonitor is applied: the hub cluster for manager metrics, the managed
cluster for default-mode agent metrics, and the hosting cluster for hosted-mode
agent metrics. If the CRD is missing, the installation or addon status reports
the apply failure.

### Creating a ManagedServiceAccount

Create a sample ManagedServiceAccount resource:

```shell
kubectl create -f - <<EOF
apiVersion: authentication.open-cluster-management.io/v1beta1
kind: ManagedServiceAccount
metadata:
  name: my-sample
  namespace: <your-cluster-name>
spec:
  rotation: {}
EOF
```

### Checking Status

After creation, the addon agent will process the ManagedServiceAccount and update its status:

```shell
kubectl get managedserviceaccount my-sample -n <your-cluster-name> -o yaml
```

Expected status output:
```yaml
status:
  conditions:
  - lastTransitionTime: "2021-12-09T09:08:15Z"
    message: ""
    reason: TokenReported
    status: "True"
    type: TokenReported
  - lastTransitionTime: "2021-12-09T09:08:15Z"
    message: ""
    reason: SecretCreated
    status: "True"
    type: SecretCreated
  expirationTimestamp: "2022-12-04T09:08:15Z"
  tokenSecretRef:
    lastRefreshTimestamp: "2021-12-09T09:08:15Z"
    name: my-sample
```

### Accessing the Service Account Token

The corresponding secret containing the service account token will be created in the same namespace:

```shell
kubectl -n <your-cluster-name> get secret my-sample
```

Expected output:
```
NAME        TYPE     DATA   AGE
my-sample   Opaque   2      2m23s
```

You can retrieve the token from the secret:

```shell
kubectl -n <your-cluster-name> get secret my-sample -o jsonpath='{.data.token}' | base64 -d
```

## Hosted Mode

Hosted mode runs the addon agent on a hosting cluster while the managed cluster
only exposes the managed service account. The hub addon manager renders the
hosted-mode workloads, so configure hosted mode through `ManagedClusterAddOn`
and `AddOnDeploymentConfig`.

### Prerequisites

Three pieces of state must exist before the addon can roll out in hosted mode:

1. On the hub, annotate the `ManagedClusterAddOn` with the hosting cluster name
   so addon-framework places hosted manifests on that cluster. Also set
   `AddOnDeploymentConfig.spec.agentInstallNamespace` to a namespace on the
   hosting cluster dedicated to this managed-serviceaccount addon instance.
   That namespace must be unique to this managed cluster:

    ```yaml
    apiVersion: addon.open-cluster-management.io/v1beta1
    kind: AddOnDeploymentConfig
    metadata:
      name: managed-serviceaccount-hosted
      namespace: <managed-cluster-name>
    spec:
      agentInstallNamespace: msa-<managed-cluster-name>
    ---
    apiVersion: addon.open-cluster-management.io/v1beta1
    kind: ManagedClusterAddOn
    metadata:
      name: managed-serviceaccount
      namespace: <managed-cluster-name>
      annotations:
        addon.open-cluster-management.io/hosting-cluster-name: <hosting-cluster-name>
        addon.open-cluster-management.io/v1alpha1-install-namespace: msa-<managed-cluster-name>
    spec:
      configs:
        - group: addon.open-cluster-management.io
          resource: addondeploymentconfigs
          namespace: <managed-cluster-name>
          name: managed-serviceaccount-hosted
    ```

   Hosted templates render fixed object names for the agent, kubeconfig
   provisioner, generated managed kubeconfig secret, and their RBAC in
   `AddonInstallNamespace`. If two managed clusters share the same hosting
   cluster namespace, their per-cluster `ManifestWork`s will contend for the
   same objects. Do not use the hosted klusterlet agent namespace here; that
   namespace owns the hosted klusterlet's bootstrap and hub kubeconfig secrets,
   and an addon namespace change can delete addon-owned namespace manifests.

2. On the hosting cluster, create the external managed kubeconfig secret in a
   namespace named after the managed cluster by default. This follows the
   hosted-addon pattern used by OCM addons such as cluster-proxy: the external
   managed kubeconfig is an input to the addon provisioner, while the addon
   install namespace is where generated runtime secrets are written. The
   provisioner reads that secret and its `kubeconfig` data key, then uses that
   kubeconfig only to mint a short-lived token for the managed service account
   and copy the addon registration hub kubeconfig secret into the hosting
   namespace. It never writes those external credentials into the generated
   managed kubeconfig secret mounted by the addon agent.

   The external managed kubeconfig must be self-contained. Embed the CA and user
   credentials inline with fields such as `certificate-authority-data`,
   `client-certificate-data`, `client-key-data`, or `token`. File-based
   `certificate-authority`, `client-certificate`, `client-key`, `tokenFile`,
   and `exec` credentials are rejected because those paths or binaries would not
   exist inside the provisioner pod.

3. On the managed cluster, grant the external managed kubeconfig identity
   permission to mint tokens for the target service account and read the addon
   registration hub kubeconfig secret. The provisioner calls
   `ServiceAccounts(<install-namespace>).CreateToken(<managed-serviceaccount-name>, ...)`,
   and copies `<addon-name>-hub-kubeconfig` into the hosting cluster so the
   hosted agent can mount `/etc/hub/kubeconfig`. The registration secret is
   created by the klusterlet after addon registration; the provisioner retries
   until it appears. The minimum RBAC is:

    ```yaml
    apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata:
      namespace: msa-<managed-cluster-name>
      name: managed-serviceaccount-kubeconfig-provisioner
    rules:
      - apiGroups: [""]
        resources: ["serviceaccounts/token"]
        resourceNames: ["managed-serviceaccount"]
        verbs: ["create"]
      - apiGroups: [""]
        resources: ["secrets"]
        resourceNames: ["managed-serviceaccount-hub-kubeconfig"]
        verbs: ["get"]
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: RoleBinding
    metadata:
      namespace: msa-<managed-cluster-name>
      name: managed-serviceaccount-kubeconfig-provisioner
    roleRef:
      apiGroup: rbac.authorization.k8s.io
      kind: Role
      name: managed-serviceaccount-kubeconfig-provisioner
    subjects:
      - kind: User
        name: <bootstrap-identity>
        apiGroup: rbac.authorization.k8s.io
    ```

### AddOnDeploymentConfig Variables

The hub manager renders hosted-mode workloads from the variables below. Each can
be overridden per managed cluster with
`AddOnDeploymentConfig.spec.customizedVariables`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ExternalManagedKubeConfigNamespace` | managed cluster name | Hosting-cluster namespace that holds the external managed kubeconfig secret. Override this only when the secret lives elsewhere. |
| `ExternalManagedKubeConfigSecret` | `external-managed-kubeconfig` | External managed kubeconfig secret name on the hosting cluster. Must contain a `kubeconfig` key. |
| `ManagedKubeConfigSecret` | `<addon-name>-managed-kubeconfig` | Hosting-cluster secret in the addon install namespace that the provisioner writes and the agent mounts at `/etc/managed/kubeconfig`. |
| `ManagedServiceAccountName` | `managed-serviceaccount` | Service account on the managed cluster whose token is minted into the generated kubeconfig. |
| `ManagedKubeConfigTokenExpirationSeconds` | `3600` | Requested `TokenRequest` lifetime in seconds. |
| `ManagedKubeConfigRefreshBefore` | `10m` | Refresh the generated secret when the token expires within this duration. |
| `ManagedKubeConfigProvisionerSyncInterval` | `5m` | Reconcile interval for the provisioner loop. |

The hub kubeconfig secret name comes from the addon-framework registration
default, `managed-serviceaccount-hub-kubeconfig`; it is not a customized
variable. The provisioner keeps the hosting-cluster copy synchronized so
registration certificate rotation is picked up by the hosted agent.

Example: point the provisioner at a non-default external managed kubeconfig
secret and lengthen the token lifetime.

```yaml
apiVersion: addon.open-cluster-management.io/v1beta1
kind: AddOnDeploymentConfig
metadata:
  name: managed-serviceaccount-hosted
  namespace: <managed-cluster-name>
spec:
  agentInstallNamespace: msa-<managed-cluster-name>
  customizedVariables:
    - name: ExternalManagedKubeConfigNamespace
      value: <managed-cluster-name>
    - name: ExternalManagedKubeConfigSecret
      value: my-managed-kubeconfig
    - name: ManagedKubeConfigTokenExpirationSeconds
      value: "7200"
    - name: ManagedKubeConfigRefreshBefore
      value: "15m"
```

Reference the config from the `ManagedClusterAddOn`:

```yaml
spec:
  configs:
    - group: addon.open-cluster-management.io
      resource: addondeploymentconfigs
      namespace: <managed-cluster-name>
      name: managed-serviceaccount-hosted
```

### Internal CLI Surface

Hosted mode adds binary CLI surface only so the addon manager can wire the
hosted-mode workloads. Configure hosted mode through `ManagedClusterAddOn` and
`AddOnDeploymentConfig`, not by invoking these commands manually.

- `msa managed-kubeconfig-provisioner` runs in the provisioner Deployment and
  pre-delete cleanup Job. Its flags are populated from the rendered manifests
  and the `AddOnDeploymentConfig` variables above.
- `msa agent` runs in the hosted agent Deployment with `/etc/hub/kubeconfig`
  for hub API calls, `--install-mode=Hosted`, and
  `--spoke-kubeconfig=/etc/managed/kubeconfig` for managed-cluster API calls.
  With `--lease-health-check=true`, the hosted-mode health lease is updated on
  the hosting cluster, matching OCM hosted add-on health checks.

## References

- Design: [https://github.com/open-cluster-management-io/enhancements/tree/main/enhancements/sig-architecture/19-projected-serviceaccount-token](https://github.com/open-cluster-management-io/enhancements/tree/main/enhancements/sig-architecture/19-projected-serviceaccount-token)
- Addon-Framework: [https://github.com/open-cluster-management-io/addon-framework](https://github.com/open-cluster-management-io/addon-framework)
