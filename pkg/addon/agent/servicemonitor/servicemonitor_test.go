package servicemonitor

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubetesting "k8s.io/client-go/testing"
)

func newFakeDiscovery(hasCRD bool) *discoveryfake.FakeDiscovery {
	fd := &discoveryfake.FakeDiscovery{
		Fake: &kubetesting.Fake{},
	}
	if hasCRD {
		fd.Resources = []*metav1.APIResourceList{
			{
				GroupVersion: "monitoring.coreos.com/v1",
				APIResources: []metav1.APIResource{
					{Name: "servicemonitors", Kind: "ServiceMonitor", Namespaced: true},
				},
			},
		}
	}
	return fd
}

func newFakeDynamic(existing ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitorList"},
		&unstructured.UnstructuredList{},
	)
	objs := make([]runtime.Object, len(existing))
	for i, o := range existing {
		objs[i] = o
	}
	return dynamicfake.NewSimpleDynamicClient(scheme, objs...)
}

func TestReconcile_CRDMissing(t *testing.T) {
	r := &Reconciler{
		DiscoveryClient:    newFakeDiscovery(false),
		DynamicClient:      newFakeDynamic(),
		Namespace:          "test-ns",
		ServiceMonitorName: "managed-serviceaccount-addon-agent",
	}

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("expected no error when CRD is missing, got: %v", err)
	}
}

func TestReconcile_CreatesServiceMonitor(t *testing.T) {
	dc := newFakeDynamic()
	r := &Reconciler{
		DiscoveryClient:    newFakeDiscovery(true),
		DynamicClient:      dc,
		Namespace:          "test-ns",
		ServiceMonitorName: "managed-serviceaccount-addon-agent",
		ServiceMonitorLabels: map[string]string{
			"release": "prometheus",
		},
	}

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	sm, err := dc.Resource(serviceMonitorGVR).Namespace("test-ns").Get(
		context.Background(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected ServiceMonitor to be created, got error: %v", err)
	}

	labels := sm.GetLabels()
	if labels["addon-agent"] != "managed-serviceaccount" {
		t.Errorf("expected label addon-agent=managed-serviceaccount, got: %v", labels)
	}
	if labels["release"] != "prometheus" {
		t.Errorf("expected label release=prometheus, got: %v", labels)
	}
}

func TestReconcile_ExistingServiceMonitor(t *testing.T) {
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("monitoring.coreos.com/v1")
	existing.SetKind("ServiceMonitor")
	existing.SetName("managed-serviceaccount-addon-agent")
	existing.SetNamespace("test-ns")
	existing.SetLabels(map[string]string{
		"addon-agent": "managed-serviceaccount",
	})
	existing.Object["spec"] = map[string]interface{}{
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

	dc := newFakeDynamic(existing)
	r := &Reconciler{
		DiscoveryClient:    newFakeDiscovery(true),
		DynamicClient:      dc,
		Namespace:          "test-ns",
		ServiceMonitorName: "managed-serviceaccount-addon-agent",
	}

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("expected no error when ServiceMonitor already exists, got: %v", err)
	}

	actions := dc.Actions()
	for _, a := range actions {
		if a.GetVerb() == "update" {
			t.Error("expected no update when spec matches, but got an update action")
		}
	}
}

func TestReconcile_UpdatesLabels(t *testing.T) {
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("monitoring.coreos.com/v1")
	existing.SetKind("ServiceMonitor")
	existing.SetName("managed-serviceaccount-addon-agent")
	existing.SetNamespace("test-ns")
	existing.SetLabels(map[string]string{
		"addon-agent": "managed-serviceaccount",
	})
	existing.Object["spec"] = map[string]interface{}{
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

	dc := newFakeDynamic(existing)
	r := &Reconciler{
		DiscoveryClient:    newFakeDiscovery(true),
		DynamicClient:      dc,
		Namespace:          "test-ns",
		ServiceMonitorName: "managed-serviceaccount-addon-agent",
		ServiceMonitorLabels: map[string]string{
			"release": "prometheus",
		},
	}

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	sm, err := dc.Resource(serviceMonitorGVR).Namespace("test-ns").Get(
		context.Background(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected ServiceMonitor to exist, got error: %v", err)
	}

	labels := sm.GetLabels()
	if labels["release"] != "prometheus" {
		t.Errorf("expected label release=prometheus after update, got: %v", labels)
	}
}

func TestReconcile_RemovesStaleLabels(t *testing.T) {
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("monitoring.coreos.com/v1")
	existing.SetKind("ServiceMonitor")
	existing.SetName("managed-serviceaccount-addon-agent")
	existing.SetNamespace("test-ns")
	existing.SetLabels(map[string]string{
		"addon-agent": "managed-serviceaccount",
		"stale-label": "should-be-removed",
	})
	existing.Object["spec"] = map[string]interface{}{
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

	dc := newFakeDynamic(existing)
	r := &Reconciler{
		DiscoveryClient:    newFakeDiscovery(true),
		DynamicClient:      dc,
		Namespace:          "test-ns",
		ServiceMonitorName: "managed-serviceaccount-addon-agent",
	}

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	sm, err := dc.Resource(serviceMonitorGVR).Namespace("test-ns").Get(
		context.Background(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected ServiceMonitor to exist, got error: %v", err)
	}

	labels := sm.GetLabels()
	if _, ok := labels["stale-label"]; ok {
		t.Errorf("expected stale-label to be removed, but it still exists: %v", labels)
	}
	if labels["addon-agent"] != "managed-serviceaccount" {
		t.Errorf("expected addon-agent label to remain, got: %v", labels)
	}
}

func TestCRDAvailable(t *testing.T) {
	tests := []struct {
		name     string
		hasCRD   bool
		expected bool
	}{
		{"CRD present", true, true},
		{"CRD absent", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{DiscoveryClient: newFakeDiscovery(tt.hasCRD)}
			if got := r.crdAvailable(); got != tt.expected {
				t.Errorf("crdAvailable() = %v, want %v", got, tt.expected)
			}
		})
	}
}
