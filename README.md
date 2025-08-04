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

### Installation Methods

The managed service account addon supports 2 installation ways:

- **default (manager - agent)**: Full deployment with both addon manager and addon agent components
- **addontemplate (only agent)**: Lightweight deployment with only the addon agent component

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

## References

- Design: [https://github.com/open-cluster-management-io/enhancements/tree/main/enhancements/sig-architecture/19-projected-serviceaccount-token](https://github.com/open-cluster-management-io/enhancements/tree/main/enhancements/sig-architecture/19-projected-serviceaccount-token)
- Addon-Framework: [https://github.com/open-cluster-management-io/addon-framework](https://github.com/open-cluster-management-io/addon-framework)
