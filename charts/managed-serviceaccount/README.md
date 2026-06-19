# Managed ServiceAccount Helm Chart

This chart installs the Managed ServiceAccount addon for Open Cluster Management
(OCM). It can deploy the hub-side addon manager, publish an `AddOnTemplate` for
addon-manager driven installs, and optionally render a `ManagedClusterAddOn` for a
specific managed cluster.

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

## Values

| Parameter | Description | Default |
| --- | --- | --- |
| `image` | Full image repository for the manager and default agent image | `quay.io/open-cluster-management/managed-serviceaccount` |
| `tag` | Image tag. Empty defaults to `v<chart version>` | `""` |
| `replicas` | Hub manager replicas, and agent replicas in `AddOnTemplate` mode | `1` |
| `agentInstallAll` | Create a `global` placement install strategy | `true` |
| `targetCluster` | Render a `ManagedClusterAddOn` directly for this cluster | `""` |
| `hubDeployMode` | Hub deployment mode: `Deployment` or `AddOnTemplate` | `Deployment` |
| `addOnTemplateName` | Name of the rendered `AddOnTemplate` | `managed-serviceaccount` |
| `featureGates.ephemeralIdentity` | Enable EphemeralIdentity behavior | `false` |
| `featureGates.clusterProfile` | Enable ClusterProfile credential sync | `false` |
| `agentImagePullSecret` | Hub namespace dockerconfigjson secret copied for agent pulls | `""` |
| `prometheus.enabled` | Render `ServiceMonitor` resources | `false` |
| `prometheus.serviceMonitor.labels` | Labels added to rendered `ServiceMonitor` resources | `{}` |

## Deployment Modes

### Hub Manager Deployment

`hubDeployMode=Deployment` installs the hub addon manager. The manager renders the
embedded agent chart for each managed cluster through addon-framework.

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set tag=latest
```

### AddOnTemplate

`hubDeployMode=AddOnTemplate` renders an `AddOnTemplate` and the hub RBAC needed by
addon-manager. When `featureGates.clusterProfile=true`, the chart also renders the
hub manager so ClusterProfile credential sync can run.

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set hubDeployMode=AddOnTemplate
```

To render a `ManagedClusterAddOn` for a specific cluster in the same install, set
`targetCluster`:

```bash
helm install managed-serviceaccount ./charts/managed-serviceaccount \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set hubDeployMode=AddOnTemplate \
  --set targetCluster=loopback
```

## Prometheus

Set `prometheus.enabled=true` to render `ServiceMonitor` resources. In
`Deployment` mode this covers the hub manager. Agent metrics in manager-driven
installs can also be enabled through `AddOnDeploymentConfig` customized variables.
In `AddOnTemplate` mode the rendered template includes the agent `ServiceMonitor`.

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
