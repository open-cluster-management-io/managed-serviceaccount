package event

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ handler.TypedEventHandler[*corev1.ServiceAccount, reconcile.Request] = &serviceAccountEventHandler[*corev1.ServiceAccount]{}

func NewServiceAccountEventHandler[T client.Object](clusterName string) serviceAccountEventHandler[T] {
	return serviceAccountEventHandler[T]{
		clusterName: clusterName,
	}
}

type serviceAccountEventHandler[T client.Object] struct {
	clusterName string
}

func (s serviceAccountEventHandler[T]) Create(ctx context.Context, event event.TypedCreateEvent[T],
	q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	s.process(event.Object.GetLabels(), event.Object.GetName(), q)
}

func (s serviceAccountEventHandler[T]) Update(ctx context.Context, event event.TypedUpdateEvent[T],
	q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (s serviceAccountEventHandler[T]) Delete(ctx context.Context, event event.TypedDeleteEvent[T],
	q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	s.process(event.Object.GetLabels(), event.Object.GetName(), q)
}

func (s serviceAccountEventHandler[T]) Generic(ctx context.Context, event event.TypedGenericEvent[T],
	q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	s.process(event.Object.GetLabels(), event.Object.GetName(), q)
}

func (s serviceAccountEventHandler[T]) process(labels map[string]string, name string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if labels[common.LabelKeyIsManagedServiceAccount] != "true" {
		return
	}
	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: s.clusterName,
			Name:      name,
		},
	})
}
