package framework

import (
	"k8s.io/apimachinery/pkg/runtime"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	authenticationv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	err := clusterv1.AddToScheme(scheme)
	if err != nil {
		setupLog.Error(err, "unable to add schema to cluster")
	}
	err = addonv1alpha1.AddToScheme(scheme)
	if err != nil {
		setupLog.Error(err, "unable to add schema to addonv1alpha1")
	}
	err = k8sscheme.AddToScheme(scheme)
	if err != nil {
		setupLog.Error(err, "unable to add schema to k8sscheme")
	}
	err = authenticationv1alpha1.AddToScheme(scheme)
	if err != nil {
		setupLog.Error(err, "unable to add schema to authenticationv1alpha1")
	}
}
