package event

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ handler.EventHandler = &secretEventHandler{}

func NewSecretEventHandler() handler.EventHandler {
	return secretEventHandler{}
}

type secretEventHandler struct {
}

func (s secretEventHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, ok := event.Object.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, ok := event.ObjectNew.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, ok := event.Object.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, ok := event.Object.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) process(secret *corev1.Secret, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if secret.Labels[common.LabelKeyIsManagedServiceAccount] != "true" {
		return
	}
	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: secret.Namespace,
			Name:      secret.Name,
		},
	})
}
