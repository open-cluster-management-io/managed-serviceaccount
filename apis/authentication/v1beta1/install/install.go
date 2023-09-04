package install

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
)

func Install(scheme *runtime.Scheme) {
	utilruntime.HandleError(v1beta1.AddToScheme(scheme))
}
