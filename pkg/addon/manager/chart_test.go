package manager_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	metricsServiceTemplate    = "managed-serviceaccount/templates/manager-metrics-service.yaml"
	metricsMonitorTemplate    = "managed-serviceaccount/templates/manager-servicemonitor.yaml"
	managerDeploymentTemplate = "managed-serviceaccount/templates/manager-deployment.yaml"
	addOnTemplateTemplate     = "managed-serviceaccount/templates/addontemplate.yaml"

	releaseNamespace      = "open-cluster-management-addon"
	metricsServiceName    = "managed-serviceaccount-addon-manager-metrics"
	metricsMonitorName    = "managed-serviceaccount-addon-manager"
	managerDeploymentName = "managed-serviceaccount-addon-manager"
	addonLabelKey         = "open-cluster-management.io/addon"
	addonLabelValue       = "managed-serviceaccount"
)

func chartDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
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
	vals, err := chartutil.ToRenderValues(c, values,
		chartutil.ReleaseOptions{Name: "msa", Namespace: releaseNamespace, IsInstall: true},
		chartutil.DefaultCapabilities)
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

func renderObject[T any](t *testing.T, out map[string]string, key string) *T {
	t.Helper()
	raw := rendered(out, key)
	if raw == "" {
		return nil
	}
	var obj T
	if err := sigsyaml.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("decode %s: %v\n%s", key, err, raw)
	}
	return &obj
}

func renderUnstructured(t *testing.T, out map[string]string, key string) *unstructured.Unstructured {
	t.Helper()
	raw := rendered(out, key)
	if raw == "" {
		return nil
	}
	jsonData, err := utilyaml.ToJSON([]byte(raw))
	if err != nil {
		t.Fatalf("convert %s to json: %v\n%s", key, err, raw)
	}
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(jsonData, obj); err != nil {
		t.Fatalf("decode %s: %v\n%s", key, err, raw)
	}
	return obj
}

func TestManagerMetricsDefaultDeploymentMode(t *testing.T) {
	out := renderChart(t, map[string]interface{}{})

	assertManagerMetricsService(t, renderObject[corev1.Service](t, out, metricsServiceTemplate), 38080)
	assertNoServiceMonitor(t, renderUnstructured(t, out, metricsMonitorTemplate))
	assertManagerDeploymentMetrics(t, renderObject[appsv1.Deployment](t, out, managerDeploymentTemplate), 38080, "Deployment")
	assertNoAddOnTemplate(t, renderUnstructured(t, out, addOnTemplateTemplate))
}

func TestManagerMetricsServiceMonitorOptIn(t *testing.T) {
	out := renderChart(t, map[string]interface{}{
		"metrics": map[string]interface{}{
			"serviceMonitor": map[string]interface{}{
				"enabled": true,
				"labels": map[string]interface{}{
					"release": "kube-prometheus",
				},
			},
		},
	})

	assertManagerMetricsService(t, renderObject[corev1.Service](t, out, metricsServiceTemplate), 38080)
	assertManagerServiceMonitor(t, renderUnstructured(t, out, metricsMonitorTemplate), map[string]string{
		"release": "kube-prometheus",
	})
}

func TestManagerMetricsSkippedWithPureAddOnTemplateMode(t *testing.T) {
	out := renderChart(t, map[string]interface{}{
		"hubDeployMode": "AddOnTemplate",
		"metrics": map[string]interface{}{
			"serviceMonitor": map[string]interface{}{
				"enabled": true,
			},
		},
	})

	assertNoService(t, renderObject[corev1.Service](t, out, metricsServiceTemplate))
	assertNoServiceMonitor(t, renderUnstructured(t, out, metricsMonitorTemplate))
	assertNoDeployment(t, renderObject[appsv1.Deployment](t, out, managerDeploymentTemplate))
	assertAddOnTemplate(t, renderUnstructured(t, out, addOnTemplateTemplate))
}

func TestManagerMetricsRenderedWithClusterProfileAddOnTemplateMode(t *testing.T) {
	out := renderChart(t, map[string]interface{}{
		"hubDeployMode": "AddOnTemplate",
		"featureGates": map[string]interface{}{
			"clusterProfile": true,
		},
		"metrics": map[string]interface{}{
			"serviceMonitor": map[string]interface{}{
				"enabled": true,
			},
		},
	})

	assertManagerMetricsService(t, renderObject[corev1.Service](t, out, metricsServiceTemplate), 38080)
	assertManagerServiceMonitor(t, renderUnstructured(t, out, metricsMonitorTemplate), nil)
	assertManagerDeploymentMetrics(t, renderObject[appsv1.Deployment](t, out, managerDeploymentTemplate), 38080, "AddOnTemplate")
	assertAddOnTemplate(t, renderUnstructured(t, out, addOnTemplateTemplate))
}

func TestManagerMetricsCustomPort(t *testing.T) {
	out := renderChart(t, map[string]interface{}{
		"metrics": map[string]interface{}{
			"port": 19090,
		},
	})

	assertManagerMetricsService(t, renderObject[corev1.Service](t, out, metricsServiceTemplate), 19090)
	assertManagerDeploymentMetrics(t, renderObject[appsv1.Deployment](t, out, managerDeploymentTemplate), 19090, "Deployment")
}

func assertManagerMetricsService(t *testing.T, service *corev1.Service, port int32) {
	t.Helper()
	if service == nil {
		t.Fatal("expected manager metrics Service to render")
	}
	if service.Name != metricsServiceName {
		t.Fatalf("unexpected Service name %q", service.Name)
	}
	if service.Namespace != releaseNamespace {
		t.Fatalf("unexpected Service namespace %q", service.Namespace)
	}
	if service.Labels[addonLabelKey] != addonLabelValue {
		t.Fatalf("unexpected Service labels %#v", service.Labels)
	}
	if !reflect.DeepEqual(service.Spec.Selector, map[string]string{addonLabelKey: addonLabelValue}) {
		t.Fatalf("unexpected Service selector %#v", service.Spec.Selector)
	}
	if len(service.Spec.Ports) != 1 {
		t.Fatalf("expected one Service port, got %#v", service.Spec.Ports)
	}
	servicePort := service.Spec.Ports[0]
	if servicePort.Name != "metrics" {
		t.Fatalf("unexpected Service port name %q", servicePort.Name)
	}
	if servicePort.Port != port {
		t.Fatalf("unexpected Service port %d", servicePort.Port)
	}
	if servicePort.TargetPort.String() != "metrics" {
		t.Fatalf("unexpected Service targetPort %q", servicePort.TargetPort.String())
	}
	if servicePort.Protocol != corev1.ProtocolTCP {
		t.Fatalf("unexpected Service protocol %q", servicePort.Protocol)
	}
}

func assertManagerServiceMonitor(t *testing.T, serviceMonitor *unstructured.Unstructured, labels map[string]string) {
	t.Helper()
	if serviceMonitor == nil {
		t.Fatal("expected manager ServiceMonitor to render")
	}
	if serviceMonitor.GetAPIVersion() != "monitoring.coreos.com/v1" {
		t.Fatalf("unexpected ServiceMonitor apiVersion %q", serviceMonitor.GetAPIVersion())
	}
	if serviceMonitor.GetKind() != "ServiceMonitor" {
		t.Fatalf("unexpected ServiceMonitor kind %q", serviceMonitor.GetKind())
	}
	if serviceMonitor.GetName() != metricsMonitorName {
		t.Fatalf("unexpected ServiceMonitor name %q", serviceMonitor.GetName())
	}
	if serviceMonitor.GetNamespace() != releaseNamespace {
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
	if !ok || !reflect.DeepEqual(selector, map[string]string{addonLabelKey: addonLabelValue}) {
		t.Fatalf("unexpected ServiceMonitor selector %#v", selector)
	}
}

func assertManagerDeploymentMetrics(t *testing.T, deployment *appsv1.Deployment, port int32, deployMode string) {
	t.Helper()
	if deployment == nil {
		t.Fatal("expected manager Deployment to render")
	}
	if deployment.Name != managerDeploymentName {
		t.Fatalf("unexpected Deployment name %q", deployment.Name)
	}
	if deployment.Namespace != releaseNamespace {
		t.Fatalf("unexpected Deployment namespace %q", deployment.Namespace)
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected one manager container, got %#v", deployment.Spec.Template.Spec.Containers)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if !contains(container.Args, "--deploy-mode="+deployMode) {
		t.Fatalf("missing deploy mode arg %q in %#v", deployMode, container.Args)
	}
	metricsArg := "--metrics-bind-address=:" + int32String(port)
	if !contains(container.Args, metricsArg) {
		t.Fatalf("missing metrics arg %q in %#v", metricsArg, container.Args)
	}
	for _, containerPort := range container.Ports {
		if containerPort.Name == "metrics" {
			if containerPort.ContainerPort != port {
				t.Fatalf("unexpected Deployment metrics containerPort %d", containerPort.ContainerPort)
			}
			if containerPort.Protocol != corev1.ProtocolTCP {
				t.Fatalf("unexpected Deployment metrics protocol %q", containerPort.Protocol)
			}
			return
		}
	}
	t.Fatalf("metrics container port not found in %#v", container.Ports)
}

func assertAddOnTemplate(t *testing.T, addOnTemplate *unstructured.Unstructured) {
	t.Helper()
	if addOnTemplate == nil {
		t.Fatal("expected AddOnTemplate to render")
	}
	if addOnTemplate.GetAPIVersion() != "addon.open-cluster-management.io/v1alpha1" {
		t.Fatalf("unexpected AddOnTemplate apiVersion %q", addOnTemplate.GetAPIVersion())
	}
	if addOnTemplate.GetKind() != "AddOnTemplate" {
		t.Fatalf("unexpected AddOnTemplate kind %q", addOnTemplate.GetKind())
	}
}

func assertNoService(t *testing.T, service *corev1.Service) {
	t.Helper()
	if service != nil {
		t.Fatalf("expected manager metrics Service to be gated off, got %#v", service)
	}
}

func assertNoServiceMonitor(t *testing.T, serviceMonitor *unstructured.Unstructured) {
	t.Helper()
	if serviceMonitor != nil {
		t.Fatalf("expected ServiceMonitor to be gated off, got %#v", serviceMonitor.Object)
	}
}

func assertNoDeployment(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()
	if deployment != nil {
		t.Fatalf("expected manager Deployment to be gated off, got %#v", deployment)
	}
}

func assertNoAddOnTemplate(t *testing.T, addOnTemplate *unstructured.Unstructured) {
	t.Helper()
	if addOnTemplate != nil {
		t.Fatalf("expected AddOnTemplate to be gated off, got %#v", addOnTemplate.Object)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func int32String(v int32) string {
	return strconv.Itoa(int(v))
}
