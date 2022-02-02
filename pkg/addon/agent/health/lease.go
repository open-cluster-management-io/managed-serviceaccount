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
	spokeClientCfg *rest.Config,
	spokeNamespace string,
) (lease.LeaseUpdater, error) {
	spokeClient, err := kubernetes.NewForConfig(spokeClientCfg)
	if err != nil {
		return nil, err
	}
	return lease.NewLeaseUpdater(
		spokeClient,
		common.AddonName,
		spokeNamespace,
	).WithHubLeaseConfig(hubClientCfg, clusterName), nil
}
