package health

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"open-cluster-management.io/addon-framework/pkg/lease"

	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

func NewAddonHealthUpdater(
	hubClientCfg *rest.Config,
	clusterName string,
	leaseClientCfg *rest.Config,
	leaseNamespace string,
) (lease.LeaseUpdater, error) {
	leaseClient, err := kubernetes.NewForConfig(leaseClientCfg)
	if err != nil {
		return nil, err
	}
	return lease.NewLeaseUpdater(
		leaseClient,
		common.AddonName,
		leaseNamespace,
	).WithHubLeaseConfig(hubClientCfg, clusterName), nil
}
