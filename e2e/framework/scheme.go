package framework

import (
	"k8s.io/apimachinery/pkg/runtime"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	authenticationv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(addonv1alpha1.AddToScheme(scheme))
	utilruntime.Must(k8sscheme.AddToScheme(scheme))
	utilruntime.Must(authenticationv1alpha1.AddToScheme(scheme))
}
