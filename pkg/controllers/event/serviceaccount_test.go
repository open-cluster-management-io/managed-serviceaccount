package event

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestServiceAccountEventHandler(t *testing.T) {
	sa := &corev1.ServiceAccount{}
	saWithLabel := &corev1.ServiceAccount{
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
				Object: sa,
			},
		},
		{
			name: "create with label",
			event: &event.CreateEvent{
				Object: saWithLabel,
			},
			queued: true,
		},
		{
			name: "update without label",
			event: &event.UpdateEvent{
				ObjectNew: sa,
			},
		},
		{
			name: "update with label",
			event: &event.UpdateEvent{
				ObjectNew: saWithLabel,
			},
		},
		{
			name: "delete without label",
			event: &event.DeleteEvent{
				Object: sa,
			},
		},
		{
			name: "delete with label",
			event: &event.DeleteEvent{
				Object: saWithLabel,
			},
			queued: true,
		},
		{
			name: "generic without label",
			event: &event.GenericEvent{
				Object: sa,
			},
		},
		{
			name: "generic with label",
			event: &event.GenericEvent{
				Object: saWithLabel,
			},
			queued: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := processEvent(NewServiceAccountEventHandler("cluster1"), c.event)
			if c.queued {
				assert.Equal(t, 1, q.Len(), "expect event queued")
			} else {
				assert.Equal(t, 0, q.Len(), "expect event ignored")
			}
		})
	}
}
