package manager_test

import (
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

const (
	addOnTemplateTemplate = "managed-serviceaccount/templates/addontemplate.yaml"

	releaseNamespace = "open-cluster-management-addon"
	agentNamespace   = "open-cluster-management-agent-addon"
	agentName        = "managed-serviceaccount-addon-agent"
	agentLabelKey    = "addon-agent"
	agentLabelValue  = "managed-serviceaccount"
)

func chartDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("unable to determine chart test file location")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", "charts", "managed-serviceaccount")
}

func renderChart(t *testing.T, values map[string]interface{}) map[string]string {
	t.Helper()
	c, err := loader.Load(chartDir(t))
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}

	vals, err := chartutil.ToRenderValues(
		c,
		values,
		chartutil.ReleaseOptions{Name: "msa", Namespace: releaseNamespace, IsInstall: true},
		chartutil.DefaultCapabilities,
	)
	if err != nil {
		t.Fatalf("compose render values: %v", err)
	}

	out, err := engine.Render(c, vals)
	if err != nil {
		t.Fatalf("render chart: %v", err)
	}
	return out
}

func rendered(out map[string]string, key string) string {
	return strings.TrimSpace(out[key])
}

func renderAddOnTemplate(t *testing.T, out map[string]string) *unstructured.Unstructured {
	t.Helper()
	raw := rendered(out, addOnTemplateTemplate)
	if raw == "" {
		return nil
	}

	obj := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("decode %s: %v\n%s", addOnTemplateTemplate, err, raw)
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestAddOnTemplateAgentMetricsDefault(t *testing.T) {
	addOnTemplate := renderAddOnTemplate(t, renderChart(t, map[string]interface{}{
		"hubDeployMode": "AddOnTemplate",
	}))

	assertAgentDeploymentMetrics(t, embeddedManifestAs[appsv1.Deployment](
		t, addOnTemplate, "apps/v1", "Deployment", agentName,
	))
	assertAgentMetricsService(t, embeddedManifestAs[corev1.Service](
		t, addOnTemplate, "v1", "Service", agentName,
	))
	assertNoEmbeddedManifest(t, addOnTemplate, "monitoring.coreos.com/v1", "ServiceMonitor", agentName)
}

func TestAddOnTemplateAgentServiceMonitorOptIn(t *testing.T) {
	addOnTemplate := renderAddOnTemplate(t, renderChart(t, map[string]interface{}{
		"hubDeployMode": "AddOnTemplate",
		"agentMetrics": map[string]interface{}{
			"serviceMonitor": map[string]interface{}{
				"enabled": true,
				"labels": map[string]interface{}{
					"release": "prometheus",
				},
			},
		},
	}))

	serviceMonitor := &unstructured.Unstructured{
		Object: findEmbeddedManifest(t, addOnTemplate, "monitoring.coreos.com/v1", "ServiceMonitor", agentName),
	}
	assertAgentServiceMonitor(t, serviceMonitor, map[string]string{
		"release": "prometheus",
	})
}

func TestDeploymentModeDoesNotRenderAddOnTemplate(t *testing.T) {
	if raw := rendered(renderChart(t, map[string]interface{}{}), addOnTemplateTemplate); raw != "" {
		t.Fatalf("expected AddOnTemplate template to be gated off in Deployment mode, got:\n%s", raw)
	}
}

func embeddedManifestAs[T any](
	t *testing.T,
	addOnTemplate *unstructured.Unstructured,
	apiVersion, kind, name string,
) *T {
	t.Helper()
	manifest := findEmbeddedManifest(t, addOnTemplate, apiVersion, kind, name)
	var obj T
	if err := apiruntime.DefaultUnstructuredConverter.FromUnstructured(manifest, &obj); err != nil {
		t.Fatalf("convert %s/%s %s: %v", apiVersion, kind, name, err)
	}
	return &obj
}

func findEmbeddedManifest(
	t *testing.T,
	addOnTemplate *unstructured.Unstructured,
	apiVersion, kind, name string,
) map[string]interface{} {
	t.Helper()
	if addOnTemplate == nil {
		t.Fatal("expected AddOnTemplate to render")
	}

	manifests, ok, err := unstructured.NestedSlice(addOnTemplate.Object, "spec", "agentSpec", "workload", "manifests")
	if err != nil {
		t.Fatalf("get AddOnTemplate workload manifests: %v", err)
	}
	if !ok {
		t.Fatal("AddOnTemplate workload manifests not found")
	}

	for _, item := range manifests {
		manifest, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("unexpected manifest item %#v", item)
		}
		if manifest["apiVersion"] != apiVersion || manifest["kind"] != kind {
			continue
		}
		metadata, ok := manifest["metadata"].(map[string]interface{})
		if !ok {
			t.Fatalf("manifest metadata is missing: %#v", manifest)
		}
		if metadata["name"] == name {
			return manifest
		}
	}

	t.Fatalf("embedded manifest %s/%s %s not found", apiVersion, kind, name)
	return nil
}

func assertNoEmbeddedManifest(
	t *testing.T,
	addOnTemplate *unstructured.Unstructured,
	apiVersion, kind, name string,
) {
	t.Helper()
	if addOnTemplate == nil {
		t.Fatal("expected AddOnTemplate to render")
	}

	manifests, ok, err := unstructured.NestedSlice(addOnTemplate.Object, "spec", "agentSpec", "workload", "manifests")
	if err != nil {
		t.Fatalf("get AddOnTemplate workload manifests: %v", err)
	}
	if !ok {
		t.Fatal("AddOnTemplate workload manifests not found")
	}

	for _, item := range manifests {
		manifest, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("unexpected manifest item %#v", item)
		}
		if manifest["apiVersion"] != apiVersion || manifest["kind"] != kind {
			continue
		}
		metadata, ok := manifest["metadata"].(map[string]interface{})
		if !ok {
			t.Fatalf("manifest metadata is missing: %#v", manifest)
		}
		if metadata["name"] == name {
			t.Fatalf("embedded manifest %s/%s %s should not render", apiVersion, kind, name)
		}
	}
}

func assertAgentDeploymentMetrics(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()
	if deployment.Name != agentName {
		t.Fatalf("unexpected Deployment name %q", deployment.Name)
	}
	if deployment.Namespace != agentNamespace {
		t.Fatalf("unexpected Deployment namespace %q", deployment.Namespace)
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected one agent container, got %#v", deployment.Spec.Template.Spec.Containers)
	}
	for _, port := range deployment.Spec.Template.Spec.Containers[0].Ports {
		if port.Name == "metrics" {
			if port.ContainerPort != 38080 {
				t.Fatalf("unexpected metrics containerPort %d", port.ContainerPort)
			}
			if port.Protocol != corev1.ProtocolTCP {
				t.Fatalf("unexpected metrics protocol %q", port.Protocol)
			}
			return
		}
	}
	t.Fatalf("metrics container port not found in %#v", deployment.Spec.Template.Spec.Containers[0].Ports)
}

func assertAgentMetricsService(t *testing.T, service *corev1.Service) {
	t.Helper()
	if service.Name != agentName {
		t.Fatalf("unexpected Service name %q", service.Name)
	}
	if service.Namespace != agentNamespace {
		t.Fatalf("unexpected Service namespace %q", service.Namespace)
	}
	if !reflect.DeepEqual(service.Labels, map[string]string{agentLabelKey: agentLabelValue}) {
		t.Fatalf("unexpected Service labels %#v", service.Labels)
	}
	if !reflect.DeepEqual(service.Spec.Selector, map[string]string{agentLabelKey: agentLabelValue}) {
		t.Fatalf("unexpected Service selector %#v", service.Spec.Selector)
	}
	if len(service.Spec.Ports) != 1 {
		t.Fatalf("expected one Service port, got %#v", service.Spec.Ports)
	}
	servicePort := service.Spec.Ports[0]
	if servicePort.Name != "metrics" {
		t.Fatalf("unexpected Service port name %q", servicePort.Name)
	}
	if servicePort.Port != 38080 {
		t.Fatalf("unexpected Service port %d", servicePort.Port)
	}
	if servicePort.TargetPort != intstr.FromString("metrics") {
		t.Fatalf("unexpected Service targetPort %q", servicePort.TargetPort.String())
	}
	if servicePort.Protocol != corev1.ProtocolTCP {
		t.Fatalf("unexpected Service protocol %q", servicePort.Protocol)
	}
}

func assertAgentServiceMonitor(t *testing.T, serviceMonitor *unstructured.Unstructured, labels map[string]string) {
	t.Helper()
	if serviceMonitor.GetAPIVersion() != "monitoring.coreos.com/v1" {
		t.Fatalf("unexpected ServiceMonitor apiVersion %q", serviceMonitor.GetAPIVersion())
	}
	if serviceMonitor.GetKind() != "ServiceMonitor" {
		t.Fatalf("unexpected ServiceMonitor kind %q", serviceMonitor.GetKind())
	}
	if serviceMonitor.GetName() != agentName {
		t.Fatalf("unexpected ServiceMonitor name %q", serviceMonitor.GetName())
	}
	if serviceMonitor.GetNamespace() != agentNamespace {
		t.Fatalf("unexpected ServiceMonitor namespace %q", serviceMonitor.GetNamespace())
	}
	if !reflect.DeepEqual(serviceMonitor.GetLabels(), labels) {
		t.Fatalf("unexpected ServiceMonitor labels %#v", serviceMonitor.GetLabels())
	}

	endpoints, ok, err := unstructured.NestedSlice(serviceMonitor.Object, "spec", "endpoints")
	if err != nil {
		t.Fatalf("get ServiceMonitor endpoints: %v", err)
	}
	if !ok || len(endpoints) != 1 {
		t.Fatalf("expected one ServiceMonitor endpoint, got %#v", endpoints)
	}
	endpoint, ok := endpoints[0].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected ServiceMonitor endpoint %#v", endpoints[0])
	}
	for key, want := range map[string]string{"path": "/metrics", "scheme": "http", "port": "metrics"} {
		if endpoint[key] != want {
			t.Fatalf("unexpected ServiceMonitor endpoint %s=%#v, want %q", key, endpoint[key], want)
		}
	}

	selector, ok, err := unstructured.NestedStringMap(serviceMonitor.Object, "spec", "selector", "matchLabels")
	if err != nil {
		t.Fatalf("get ServiceMonitor selector: %v", err)
	}
	if !ok || !reflect.DeepEqual(selector, map[string]string{agentLabelKey: agentLabelValue}) {
		t.Fatalf("unexpected ServiceMonitor selector %#v", selector)
	}
}
