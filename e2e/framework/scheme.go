package framework

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1alpha1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	cpv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(addonv1alpha1.AddToScheme(scheme))
	utilruntime.Must(k8sscheme.AddToScheme(scheme))
	utilruntime.Must(authv1alpha1.AddToScheme(scheme))
	utilruntime.Must(authv1beta1.AddToScheme(scheme))
	utilruntime.Must(cpv1alpha1.AddToScheme(scheme))
}
