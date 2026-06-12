package manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/utils"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

func TestNewRegistrationOption(t *testing.T) {
	clusterName := "cluster1"
	fakeKubeClient := fakekube.NewSimpleClientset()

	registrationOptions := NewRegistrationOption(fakeKubeClient)
	assert.NotNil(t, registrationOptions.PermissionConfig, "permissionConfig is not specified")

	err := registrationOptions.PermissionConfig(newTestCluster(clusterName), newTestAddOn("addon", clusterName))
	assert.NoError(t, err)

	actions := fakeKubeClient.Actions()
	assert.Len(t, actions, 2)
	role := actions[0].(clienttesting.CreateAction).GetObject().(*rbacv1.Role)
	assert.Equal(t, clusterName, role.Namespace, "invalid role ns")
	assert.Equal(t, "managed-serviceaccount-addon-agent", role.Name, "invalid role name")
	rolebinding := actions[1].(clienttesting.CreateAction).GetObject().(*rbacv1.RoleBinding)
	assert.Equal(t, clusterName, rolebinding.Namespace, "invalid rolebinding ns")
	assert.Equal(t, "managed-serviceaccount-addon-agent", rolebinding.Name, "invalid rolebinding name")
}

func TestManifestAddonAgent(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	manifestNames := []string{
		addonName,
		"managed-serviceaccount",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"managed-serviceaccount-addon-agent",
		"managed-serviceaccount-addon-agent",
	}

	cases := []struct {
		name                  string
		getValuesFunc         []addonfactory.GetValuesFunc
		expectedManifestNames []string
	}{
		{
			name:                  "install",
			getValuesFunc:         []addonfactory.GetValuesFunc{GetDefaultValues(imageName, nil)},
			expectedManifestNames: manifestNames,
		},
		{
			name:                  "install all with image pull secret",
			getValuesFunc:         []addonfactory.GetValuesFunc{GetDefaultValues(imageName, newTestImagePullSecret())},
			expectedManifestNames: append(manifestNames, "open-cluster-management-image-pull-credentials"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, FS, "manifests/templates").
				WithGetValuesFuncs(c.getValuesFunc...)

			addOnAgent, err := agentFactory.BuildTemplateAgentAddon()
			assert.NoError(t, err)

			manifests, err := addOnAgent.Manifests(newTestCluster(clusterName), newTestAddOn(addonName, clusterName))
			assert.NoError(t, err)

			actual := []string{}
			for _, manifest := range manifests {
				obj, ok := manifest.(metav1.ObjectMetaAccessor)
				assert.True(t, ok, "invalid manifest")
				if ns := obj.GetObjectMeta().GetNamespace(); len(ns) > 0 {
					assert.Equalf(t, addonName, ns, "unexpected ns of manifest %q", obj.GetObjectMeta().GetName())
				}
				actual = append(actual, obj.GetObjectMeta().GetName())
			}
			assert.ElementsMatch(t, c.expectedManifestNames, actual)
		})
	}
}

func TestManifestAddonAgentMetricsServiceDefaultEnabled(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestAddOn(addonName, clusterName),
		GetDefaultValues(imageName, nil),
	)

	assertDeploymentContainerPort(t, findDeployment(t, manifests, "managed-serviceaccount-addon-agent"), "metrics", 38080)
	assertAgentMetricsService(t, findService(t, manifests, "managed-serviceaccount-addon-agent"))
	assertNoServiceMonitor(t, manifests, "managed-serviceaccount-addon-agent")
}

func TestManifestAddonAgentServiceMonitorOptIn(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestAddOn(addonName, clusterName),
		GetDefaultValues(imageName, nil),
		func(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"AgentServiceMonitorEnabled": "true",
			}, nil
		},
	)

	assertAgentMetricsService(t, findService(t, manifests, "managed-serviceaccount-addon-agent"))
	serviceMonitor := findServiceMonitor(t, manifests, "managed-serviceaccount-addon-agent")
	assertAgentServiceMonitor(t, serviceMonitor)
	assert.Empty(t, serviceMonitor.GetLabels())
}

func TestManifestAddonAgentServiceMonitorLabelsFromAddOnDeploymentConfig(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestAddOn(addonName, clusterName)
	config := &addonv1alpha1.AddOnDeploymentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metrics-config",
			Namespace: clusterName,
		},
		Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
			CustomizedVariables: []addonv1alpha1.CustomizedVariable{
				{Name: "AgentServiceMonitorEnabled", Value: "true"},
				{Name: "AgentServiceMonitorLabels", Value: "release=prometheus,team=platform"},
			},
		},
	}
	addon.Status.ConfigReferences = []addonv1alpha1.ConfigReference{
		{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    utils.AddOnDeploymentConfigGVR.Group,
				Resource: utils.AddOnDeploymentConfigGVR.Resource,
			},
			DesiredConfig: &addonv1alpha1.ConfigSpecHash{
				ConfigReferent: addonv1alpha1.ConfigReferent{
					Namespace: config.Namespace,
					Name:      config.Name,
				},
				SpecHash: "hash",
			},
		},
	}

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		addon,
		GetDefaultValues(imageName, nil),
		addonfactory.GetAddOnDeploymentConfigValues(
			fakeAddOnDeploymentConfigGetter{config: config},
			ToAddOnDeploymentConfigValues,
		),
	)

	serviceMonitor := findServiceMonitor(t, manifests, "managed-serviceaccount-addon-agent")
	assertAgentServiceMonitor(t, serviceMonitor)
	assert.Equal(t, map[string]string{
		"release": "prometheus",
		"team":    "platform",
	}, serviceMonitor.GetLabels())
}

func renderTestManifests(
	t *testing.T,
	cluster *clusterv1.ManagedCluster,
	addon *addonv1alpha1.ManagedClusterAddOn,
	getValuesFuncs ...addonfactory.GetValuesFunc,
) []runtime.Object {
	t.Helper()

	agentFactory := NewAgentAddonFactory(common.AddonName, FS, "manifests/templates").
		WithGetValuesFuncs(getValuesFuncs...)

	addOnAgent, err := agentFactory.BuildTemplateAgentAddon()
	assert.NoError(t, err)

	manifests, err := addOnAgent.Manifests(cluster, addon)
	assert.NoError(t, err)

	return manifests
}

type fakeAddOnDeploymentConfigGetter struct {
	config *addonv1alpha1.AddOnDeploymentConfig
}

func (g fakeAddOnDeploymentConfigGetter) Get(ctx context.Context, namespace, name string) (*addonv1alpha1.AddOnDeploymentConfig, error) {
	if g.config == nil {
		return nil, fmt.Errorf("addon deployment config %s/%s not found", namespace, name)
	}
	if g.config.Namespace != namespace || g.config.Name != name {
		return nil, fmt.Errorf("addon deployment config %s/%s not found", namespace, name)
	}
	return g.config, nil
}

func findDeployment(t *testing.T, manifests []runtime.Object, name string) *appsv1.Deployment {
	t.Helper()
	for _, manifest := range manifests {
		deployment, ok := manifest.(*appsv1.Deployment)
		if ok && deployment.Name == name {
			return deployment
		}
	}
	t.Fatalf("deployment %q not found", name)
	return nil
}

func findService(t *testing.T, manifests []runtime.Object, name string) *corev1.Service {
	t.Helper()
	for _, manifest := range manifests {
		service, ok := manifest.(*corev1.Service)
		if ok && service.Name == name {
			return service
		}
	}
	t.Fatalf("service %q not found", name)
	return nil
}

func findServiceMonitor(t *testing.T, manifests []runtime.Object, name string) *unstructured.Unstructured {
	t.Helper()
	for _, manifest := range manifests {
		serviceMonitor, ok := manifest.(*unstructured.Unstructured)
		if ok && serviceMonitor.GetKind() == "ServiceMonitor" && serviceMonitor.GetName() == name {
			return serviceMonitor
		}
	}
	t.Fatalf("servicemonitor %q not found", name)
	return nil
}

func assertNoServiceMonitor(t *testing.T, manifests []runtime.Object, name string) {
	t.Helper()
	for _, manifest := range manifests {
		serviceMonitor, ok := manifest.(*unstructured.Unstructured)
		if ok && serviceMonitor.GetKind() == "ServiceMonitor" && serviceMonitor.GetName() == name {
			t.Fatalf("servicemonitor %q should not be rendered", name)
		}
	}
}

func assertDeploymentContainerPort(t *testing.T, deployment *appsv1.Deployment, name string, port int32) {
	t.Helper()
	for _, containerPort := range deployment.Spec.Template.Spec.Containers[0].Ports {
		if containerPort.Name == name {
			assert.Equal(t, port, containerPort.ContainerPort)
			assert.Equal(t, corev1.ProtocolTCP, containerPort.Protocol)
			return
		}
	}
	t.Fatalf("container port %q not found", name)
}

func assertAgentMetricsService(t *testing.T, service *corev1.Service) {
	t.Helper()
	assert.Equal(t, "managed-serviceaccount-addon-agent", service.Name)
	assert.Equal(t, map[string]string{"addon-agent": "managed-serviceaccount"}, service.Labels)
	assert.Equal(t, map[string]string{"addon-agent": "managed-serviceaccount"}, service.Spec.Selector)
	if assert.Len(t, service.Spec.Ports, 1) {
		assert.Equal(t, "metrics", service.Spec.Ports[0].Name)
		assert.Equal(t, int32(38080), service.Spec.Ports[0].Port)
		assert.Equal(t, intstr.FromString("metrics"), service.Spec.Ports[0].TargetPort)
		assert.Equal(t, corev1.ProtocolTCP, service.Spec.Ports[0].Protocol)
	}
}

func assertAgentServiceMonitor(t *testing.T, serviceMonitor *unstructured.Unstructured) {
	t.Helper()
	assert.Equal(t, "monitoring.coreos.com/v1", serviceMonitor.GetAPIVersion())
	assert.Equal(t, "ServiceMonitor", serviceMonitor.GetKind())

	endpoints, ok, err := unstructured.NestedSlice(serviceMonitor.Object, "spec", "endpoints")
	assert.NoError(t, err)
	if assert.True(t, ok, "ServiceMonitor endpoints should be set") && assert.Len(t, endpoints, 1) {
		endpoint, ok := endpoints[0].(map[string]interface{})
		if assert.True(t, ok, "endpoint should be an object") {
			assert.Equal(t, "/metrics", endpoint["path"])
			assert.Equal(t, "http", endpoint["scheme"])
			assert.Equal(t, "metrics", endpoint["port"])
		}
	}

	selector, ok, err := unstructured.NestedStringMap(serviceMonitor.Object, "spec", "selector", "matchLabels")
	assert.NoError(t, err)
	if assert.True(t, ok, "ServiceMonitor selector should be set") {
		assert.Equal(t, map[string]string{"addon-agent": "managed-serviceaccount"}, selector)
	}
}

func newTestImagePullSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test",
		},
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte("test"),
		},
	}
}

func newTestCluster(name string) *clusterv1.ManagedCluster {
	return &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func newTestAddOn(name, namespace string) *addonv1alpha1.ManagedClusterAddOn {
	return &addonv1alpha1.ManagedClusterAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: addonv1alpha1.ManagedClusterAddOnSpec{
			InstallNamespace: name,
		},
	}
}
