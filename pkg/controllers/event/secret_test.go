package event

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestSecretEventHandler(t *testing.T) {
	secret := &corev1.Secret{}
	secretWithLabel := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				common.LabelKeyIsManagedServiceAccount: "true",
			},
		},
	}

	cases := []struct {
		name   string
		event  interface{}
		queued bool
	}{
		{
			name: "create without label",
			event: &event.CreateEvent{
				Object: secret,
			},
		},
		{
			name: "create with label",
			event: &event.CreateEvent{
				Object: secretWithLabel,
			},
			queued: true,
		},
		{
			name: "update without label",
			event: &event.UpdateEvent{
				ObjectNew: secret,
			},
		},
		{
			name: "update with label",
			event: &event.UpdateEvent{
				ObjectNew: secretWithLabel,
			},
			queued: true,
		},
		{
			name: "delete without label",
			event: &event.DeleteEvent{
				Object: secret,
			},
		},
		{
			name: "delete with label",
			event: &event.DeleteEvent{
				Object: secretWithLabel,
			},
			queued: true,
		},
		{
			name: "generic without label",
			event: &event.GenericEvent{
				Object: secret,
			},
		},
		{
			name: "generic with label",
			event: &event.GenericEvent{
				Object: secretWithLabel,
			},
			queued: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := processEvent(NewSecretEventHandler(), c.event)
			if c.queued {
				assert.Equal(t, 1, q.Len(), "expect event queued")
			} else {
				assert.Equal(t, 0, q.Len(), "expect event ignored")
			}
		})
	}
}

func processEvent(handler handler.TypedEventHandler[client.Object, reconcile.Request], evt interface{}) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	q := &controllertest.Queue{TypedInterface: workqueue.NewTyped[reconcile.Request]()}
	switch e := evt.(type) {
	case *event.CreateEvent:
		handler.Create(context.TODO(), *e, q)
	case *event.UpdateEvent:
		handler.Update(context.TODO(), *e, q)
	case *event.DeleteEvent:
		handler.Delete(context.TODO(), *e, q)
	case *event.GenericEvent:
		handler.Generic(context.TODO(), *e, q)
	}
	return q
}
