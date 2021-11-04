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

var _ handler.EventHandler = &secretEventHandler{}

func NewSecretEventHandler() handler.EventHandler {
	return secretEventHandler{}
}

type secretEventHandler struct {
}

func (s secretEventHandler) Create(event event.CreateEvent, q workqueue.RateLimitingInterface) {
	secret, ok := event.Object.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) Update(event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	secret, ok := event.ObjectNew.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) Delete(event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	secret, ok := event.Object.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) Generic(event event.GenericEvent, q workqueue.RateLimitingInterface) {
	secret, ok := event.Object.(*corev1.Secret)
	if ok {
		s.process(secret, q)
	}
}

func (s secretEventHandler) process(secret *corev1.Secret, q workqueue.RateLimitingInterface) {
	if secret.Labels[common.LabelKeyIsManagedServiceAccount] != "true" {
		return
	}
	managedServiceAccountNamespace := secret.Labels[common.LabelKeyManagedServiceAccountNamespace]
	managedServiceAccountName := secret.Labels[common.LabelKeyManagedServiceAccountName]
	if len(managedServiceAccountNamespace) > 0 && len(managedServiceAccountName) > 0 {
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: managedServiceAccountNamespace,
				Name:      managedServiceAccountName,
			},
		})
	}

}
