# Managed ServiceAccount Helm Chart

This chart installs the Managed ServiceAccount addon for Open Cluster Management
(OCM). It deploys the hub-side addon manager and can optionally render a
`ManagedClusterAddOn` for a specific managed cluster.

## Prerequisites

- Kubernetes cluster with Open Cluster Management installed
- Helm 3.x
- The `ManagedServiceAccount` CRD installed by this chart, or installable by Helm
- The Prometheus Operator `ServiceMonitor` CRD when `prometheus.enabled=true`

## Installation

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace
```

By default, the chart installs the hub manager in `Deployment` mode and configures
the `ClusterManagementAddOn` to install the addon on all managed clusters through
the `global` placement.

For placement-managed installs, agents use the addon-framework default namespace
`open-cluster-management-agent-addon` unless an `AddOnDeploymentConfig` override
selects another namespace.

## Values

| Parameter | Description | Default |
| --- | --- | --- |
| `image` | Full image repository for the manager and default agent image | `quay.io/open-cluster-management/managed-serviceaccount` |
| `tag` | Image tag. Empty defaults to `v<chart version>` | `""` |
| `replicas` | Hub manager replicas | `1` |
| `agentInstallAll` | Create a `global` placement install strategy | `true` |
| `targetCluster` | Render a `ManagedClusterAddOn` directly for this cluster | `""` |
| `featureGates.ephemeralIdentity` | Enable EphemeralIdentity behavior | `false` |
| `featureGates.clusterProfile` | Enable ClusterProfile credential sync | `false` |
| `agentImagePullSecret` | Hub namespace dockerconfigjson secret copied for agent pulls | `""` |
| `prometheus.enabled` | Render `ServiceMonitor` resources | `false` |
| `prometheus.serviceMonitor.labels` | Labels added to rendered `ServiceMonitor` resources | `{}` |

## Hub Manager

The chart installs the hub addon manager. The manager renders the embedded agent
chart for each managed cluster through addon-framework.

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set tag=latest
```

To render a `ManagedClusterAddOn` for a specific cluster in the same install, set
`targetCluster`:

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set targetCluster=loopback
```

## Prometheus

Set `prometheus.enabled=true` to render `ServiceMonitor` resources. In
the chart this covers the hub manager. Agent metrics can also be enabled through
`AddOnDeploymentConfig` customized variables.

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set prometheus.enabled=true \
  --set prometheus.serviceMonitor.labels.release=prometheus
```

## Image Overrides

Use `image` and `tag` to override the default chart image:

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set image=quay.io/example/managed-serviceaccount \
  --set tag=v1.2.3
```

If the agent image needs pull credentials, create a `kubernetes.io/dockerconfigjson`
secret in the release namespace and pass its name with `agentImagePullSecret`.

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set agentImagePullSecret=managed-serviceaccount-pull-secret
```

## Uninstallation

```bash
helm uninstall managed-serviceaccount --namespace open-cluster-management-addon
```
