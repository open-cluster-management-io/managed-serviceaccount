package managedserviceaccount_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	releaseNamespace    = "open-cluster-management-addon"
	helmTemplateTimeout = 30 * time.Second
)

func TestClusterManagementAddOnRendersPlacementStrategyWithoutDefaultConfig(t *testing.T) {
	objects := renderChart(t)
	cma := findObject(t, objects, "ClusterManagementAddOn", "", "managed-serviceaccount")

	assertNestedFieldNotFound(t, cma.Object, "spec", "defaultConfigs")

	placements := mustNestedSlice(t, cma.Object, "spec", "installStrategy", "placements")
	assertLen(t, placements, 1, "placements")

	placement, ok := placements[0].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected placement type %T", placements[0])
	}
	assertNestedFieldNotFound(t, placement, "configs")
}

func TestClusterManagementAddOnWithoutInstallAllOmitsInstallStrategyAndDefaultConfig(t *testing.T) {
	objects := renderChart(t, "--set", "agentInstallAll=false")
	cma := findObject(t, objects, "ClusterManagementAddOn", "", "managed-serviceaccount")

	assertNestedFieldNotFound(t, cma.Object, "spec", "installStrategy")
	assertNestedFieldNotFound(t, cma.Object, "spec", "defaultConfigs")
}

func TestTargetClusterManagedClusterAddOnRendersConfig(t *testing.T) {
	objects := renderChart(t, "--set", "targetCluster=loopback")
	mca := findObject(t, objects, "ManagedClusterAddOn", "loopback", "managed-serviceaccount")

	configs := mustNestedSlice(t, mca.Object, "spec", "configs")
	assertLen(t, configs, 1, "managedClusterAddOn configs")
	assertAddOnDeploymentConfigRef(t, configs[0], "loopback")
}

func renderChart(t *testing.T, args ...string) []*unstructured.Unstructured {
	t.Helper()

	helmPath := os.Getenv("HELM_PATH")
	if helmPath == "" {
		helmPath = "helm"
	}

	baseArgs := []string{
		"template",
		"managed-serviceaccount",
		".",
		"--namespace",
		releaseNamespace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), helmTemplateTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, helmPath, append(baseArgs, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("helm template timed out after %s\n%s", helmTemplateTimeout, output)
		}
		t.Fatalf("helm template failed: %v\n%s", err, output)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(output), 4096)
	var objects []*unstructured.Unstructured
	for {
		object := map[string]interface{}{}
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to decode helm output: %v", err)
		}
		if len(object) == 0 {
			continue
		}
		objects = append(objects, &unstructured.Unstructured{Object: object})
	}

	return objects
}

func findObject(
	t *testing.T,
	objects []*unstructured.Unstructured,
	kind, namespace, name string,
) *unstructured.Unstructured {
	t.Helper()

	for _, object := range objects {
		if object.GetKind() == kind && object.GetNamespace() == namespace && object.GetName() == name {
			return object
		}
	}
	t.Fatalf("object %s/%s %s not found", namespace, name, kind)
	return nil
}

func mustNestedSlice(t *testing.T, object map[string]interface{}, fields ...string) []interface{} {
	t.Helper()

	values, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil {
		t.Fatalf("failed to read field %v: %v", fields, err)
	}
	if !found {
		t.Fatalf("missing field %v", fields)
	}
	return values
}

func assertNestedFieldNotFound(t *testing.T, object map[string]interface{}, fields ...string) {
	t.Helper()

	_, found, err := unstructured.NestedFieldNoCopy(object, fields...)
	if err != nil {
		t.Fatalf("failed to read field %v: %v", fields, err)
	}
	if found {
		t.Fatalf("field %v should not be rendered", fields)
	}
}

func assertLen(t *testing.T, values []interface{}, want int, name string) {
	t.Helper()

	if len(values) != want {
		t.Fatalf("unexpected %s length %d, want %d", name, len(values), want)
	}
}

func assertAddOnDeploymentConfigRef(t *testing.T, raw interface{}, namespace string) {
	t.Helper()

	ref, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected config ref type %T", raw)
	}
	if ref["group"] != "addon.open-cluster-management.io" {
		t.Fatalf("unexpected config ref group %v", ref["group"])
	}
	if ref["resource"] != "addondeploymentconfigs" {
		t.Fatalf("unexpected config ref resource %v", ref["resource"])
	}
	if ref["namespace"] != namespace {
		t.Fatalf("unexpected config ref namespace %v, want %s", ref["namespace"], namespace)
	}
	if ref["name"] != "managed-serviceaccount" {
		t.Fatalf("unexpected config ref name %v", ref["name"])
	}
}
