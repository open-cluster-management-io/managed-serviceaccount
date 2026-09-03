package servicemonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

var serviceMonitorGVR = schema.GroupVersionResource{
	Group:    "monitoring.coreos.com",
	Version:  "v1",
	Resource: "servicemonitors",
}

// Reconciler ensures a ServiceMonitor exists on the managed cluster when the
// monitoring.coreos.com CRD is available. If the CRD is absent it logs a
// message and returns without error, making it safe to run on any cluster.
type Reconciler struct {
	DiscoveryClient     discovery.DiscoveryInterface
	DynamicClient       dynamic.Interface
	Namespace           string
	ServiceMonitorName  string
	ServiceMonitorLabels map[string]string
}

// Start runs the ServiceMonitor reconciliation loop, retrying every 5 minutes.
// It blocks until the context is cancelled.
func (r *Reconciler) Start(ctx context.Context) {
	logger := klog.FromContext(ctx)

	// Retry periodically so that if the Prometheus Operator is installed
	// after the agent starts, the ServiceMonitor will eventually be created.
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		if err := r.reconcile(ctx); err != nil {
			logger.Error(err, "ServiceMonitor reconciliation failed, will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reconcile performs a single reconciliation pass: checks the CRD, then
// creates or updates the ServiceMonitor to match the desired state.
func (r *Reconciler) reconcile(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	if !r.crdAvailable() {
		logger.V(4).Info("ServiceMonitor CRD not found on managed cluster, skipping ServiceMonitor creation")
		return nil
	}

	desired := r.buildServiceMonitor()
	client := r.DynamicClient.Resource(serviceMonitorGVR).Namespace(r.Namespace)

	existing, err := client.Get(ctx, r.ServiceMonitorName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get ServiceMonitor: %w", err)
		}
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create ServiceMonitor: %w", err)
		}
		logger.Info("Created ServiceMonitor", "name", r.ServiceMonitorName, "namespace", r.Namespace)
		return nil
	}

	// Update if spec or labels drifted.
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	desiredLabels := desired.GetLabels()
	existingLabels := existing.GetLabels()

	needsUpdate := false
	if !specEqual(desiredSpec, existingSpec) {
		existing.Object["spec"] = desired.Object["spec"]
		needsUpdate = true
	}
	if !labelsMatch(desiredLabels, existingLabels) {
		existing.SetLabels(desiredLabels)
		needsUpdate = true
	}
	if needsUpdate {
		if _, err := client.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update ServiceMonitor: %w", err)
		}
		logger.Info("Updated ServiceMonitor", "name", r.ServiceMonitorName, "namespace", r.Namespace)
	}

	return nil
}

// crdAvailable returns true if the monitoring.coreos.com/v1 ServiceMonitor
// CRD is registered on the managed cluster.
func (r *Reconciler) crdAvailable() bool {
	resources, err := r.DiscoveryClient.ServerResourcesForGroupVersion("monitoring.coreos.com/v1")
	if err != nil {
		return false
	}
	for _, res := range resources.APIResources {
		if res.Kind == "ServiceMonitor" {
			return true
		}
	}
	return false
}

// buildServiceMonitor returns the desired ServiceMonitor as an unstructured
// object, populated from the reconciler's configuration.
func (r *Reconciler) buildServiceMonitor() *unstructured.Unstructured {
	sm := &unstructured.Unstructured{}
	sm.SetAPIVersion("monitoring.coreos.com/v1")
	sm.SetKind("ServiceMonitor")
	sm.SetName(r.ServiceMonitorName)
	sm.SetNamespace(r.Namespace)

	labels := map[string]string{
		"addon-agent": "managed-serviceaccount",
	}
	for k, v := range r.ServiceMonitorLabels {
		labels[k] = v
	}
	sm.SetLabels(labels)

	spec := map[string]interface{}{
		"endpoints": []interface{}{
			map[string]interface{}{
				"path":   "/metrics",
				"scheme": "http",
				"port":   "metrics",
			},
		},
		"selector": map[string]interface{}{
			"matchLabels": map[string]interface{}{
				"addon-agent": "managed-serviceaccount",
			},
		},
	}
	sm.Object["spec"] = spec

	return sm
}

// specEqual returns true if two unstructured maps are JSON-equivalent.
func specEqual(a, b map[string]interface{}) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// labelsMatch returns true if the desired and existing label maps are exactly
// equal. This ensures stale labels are detected and removed during reconciliation.
func labelsMatch(desired, existing map[string]string) bool {
	if len(desired) != len(existing) {
		return false
	}
	for k, v := range desired {
		if existing[k] != v {
			return false
		}
	}
	return true
}
