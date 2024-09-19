# Managed Service Account

## What is Managed Service Account?

"Managed Service Account" is an OCM addon developed over [addon-framework](https://github.com/open-cluster-management-io/addon-framework)
for synchronizing [ServiceAccount](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/)
to the managed clusters and collecting the tokens from these local service
accounts as secret resources back to the hub cluster. This addon will be 
helpful when you're:

- Ensuring service account resources to the managed clusters w/o a kubeconfig
  to the managed cluster.
- Accessing the kube api of the managed clusters from the hub cluster which
  will require legit authentication tokens.
- Homogenizing the client identity to the same service account when requesting
  the managed clusters' api.
  
The addon basically consists of two components following the typical 
architecture of an OCM [addon](https://open-cluster-management.io/concepts/addon/):

- "Addon-Manager": Automatically installs the addon agent into the managed 
  cluster and related required resources.
  
- "Addon-Agent": Watching the "ManagedServiceAccount" API and projecting the 
  service account token periodically as secret resources to the hub cluster.
  And refreshes the tokens as well according to the rotation policy.

## Install

### Prerequisite

- OCM registration (>= 0.5.0)

### Steps

Installing the addons via the helm charts:

```shell
$ helm repo add ocm https://open-cluster-management.io/helm-charts/
$ helm repo update
$ helm search repo ocm/managed-serviceaccount
NAME                       	CHART VERSION	APP VERSION	DESCRIPTION                   
ocm/managed-serviceaccount  <...>       	1.0.0      	A Helm chart for Managed ServiceAccount Addon 
$ helm install \
    -n open-cluster-management-addon --create-namespace \
    managed-serviceaccount ocm/managed-serviceaccount
```

To confirm the installation status:

```shell
$ kubectl get managedclusteraddon -A | grep managed-serviceaccount
NAMESPACE        NAME                     AVAILABLE   DEGRADED   PROGRESSING
<your cluster>   managed-serviceaccount   True 
```

## Usage

Apply a sample "ManagedServiceAccount" resource to try the functionality:

```shell
$ kubectl create -f - <<EOF
apiVersion: authentication.open-cluster-management.io/v1beta1
kind: ManagedServiceAccount
metadata:
  name: my-sample
  namespace: <your cluster>
spec:
  rotation: {}
EOF
```

Then the addon agent is supposed to process the "ManagedServiceAccount" and
report the status:

```yaml
...
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

Corresponding secret containing the service account token should be persisted 
under the same namespace where the "ManagedServiceAccount" resource at:

```shell
$ kubectl -n <your cluster> get secret my-sample  
NAME        TYPE     DATA   AGE
my-sample   Opaque   2      2m23s
```

## References

- Design: [https://github.com/open-cluster-management-io/enhancements/tree/main/enhancements/sig-architecture/19-projected-serviceaccount-token](https://github.com/open-cluster-management-io/enhancements/tree/main/enhancements/sig-architecture/19-projected-serviceaccount-token)
- Addon-Framework: [https://github.com/open-cluster-management-io/addon-framework](https://github.com/open-cluster-management-io/addon-framework)
