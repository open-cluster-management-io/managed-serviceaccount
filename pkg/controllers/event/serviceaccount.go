package event

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ handler.EventHandler = &serviceAccountEventHandler{}

func NewServiceAccountEventHandler(clusterName string) handler.EventHandler {
	return serviceAccountEventHandler{
		clusterName: clusterName,
	}
}

type serviceAccountEventHandler struct {
	clusterName string
}

func (s serviceAccountEventHandler) Create(event event.CreateEvent, q workqueue.RateLimitingInterface) {
	serviceAccount, ok := event.Object.(*corev1.ServiceAccount)
	if ok {
		s.process(serviceAccount, q)
	}
}

func (s serviceAccountEventHandler) Update(event event.UpdateEvent, q workqueue.RateLimitingInterface) {
}

func (s serviceAccountEventHandler) Delete(event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	serviceAccount, ok := event.Object.(*corev1.ServiceAccount)
	if ok {
		s.process(serviceAccount, q)
	}
}

func (s serviceAccountEventHandler) Generic(event event.GenericEvent, q workqueue.RateLimitingInterface) {
	serviceAccount, ok := event.Object.(*corev1.ServiceAccount)
	if ok {
		s.process(serviceAccount, q)
	}
}

func (s serviceAccountEventHandler) process(serviceAccount *corev1.ServiceAccount, q workqueue.RateLimitingInterface) {
	if serviceAccount.Labels[common.LabelKeyIsManagedServiceAccount] != "true" {
		return
	}
	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: s.clusterName,
			Name:      serviceAccount.Name,
		},
	})
}
