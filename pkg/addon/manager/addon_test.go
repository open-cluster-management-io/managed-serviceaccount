package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
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

func TestManifestOrphan(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	cases := []struct {
		name             string
		getValuesFunc    []addonfactory.GetValuesFunc
		installNamespace string
		validate         func(t *testing.T, manifests []runtime.Object)
	}{
		{
			name:             "install namespace is open-cluster-management-agent-addon",
			getValuesFunc:    []addonfactory.GetValuesFunc{GetDefaultValues(imageName, newTestImagePullSecret())},
			installNamespace: "open-cluster-management-agent-addon",
			validate: func(t *testing.T, manifests []runtime.Object) {
				nsFound := false
				secretFound := false
				for _, manifest := range manifests {
					obj, ok := manifest.(metav1.ObjectMetaAccessor)
					assert.True(t, ok, "invalid manifest")

					namespace, nok := obj.(*corev1.Namespace)
					if nok {
						assert.Equal(t, map[string]string{"addon.open-cluster-management.io/deletion-orphan": ""},
							namespace.Annotations, "invalid namespace annotations")
						nsFound = true
						continue
					}

					secret, sok := obj.(*corev1.Secret)
					if sok {
						if secret.Name == "open-cluster-management-image-pull-credentials" {
							secretFound = true
						}
					}
				}
				assert.True(t, nsFound, "namespace not found")
				assert.False(t, secretFound, "pull secret found")
			},
		},
		{
			name:             "install namespace is not open-cluster-management-agent-addon",
			getValuesFunc:    []addonfactory.GetValuesFunc{GetDefaultValues(imageName, newTestImagePullSecret())},
			installNamespace: "test",
			validate: func(t *testing.T, manifests []runtime.Object) {
				nsFound := false
				secretFound := false
				for _, manifest := range manifests {
					obj, ok := manifest.(metav1.ObjectMetaAccessor)
					assert.True(t, ok, "invalid manifest")

					namespace, nok := obj.(*corev1.Namespace)
					if nok {
						assert.Nil(t, namespace.Annotations, "invalid namespace annotations")
						nsFound = true
						continue
					}

					secret, sok := obj.(*corev1.Secret)
					if sok {
						if secret.Name == "open-cluster-management-image-pull-credentials" {
							secretFound = true
						}
					}
				}
				assert.True(t, nsFound, "namespace not found")
				assert.True(t, secretFound, "pull secret not found")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, FS, "manifests/templates").
				WithGetValuesFuncs(c.getValuesFunc...)

			addOnAgent, err := agentFactory.BuildTemplateAgentAddon()
			assert.NoError(t, err)

			manifests, err := addOnAgent.Manifests(newTestCluster(clusterName),
				newTestAddOnWithInstallNamespace(addonName, clusterName, c.installNamespace))
			assert.NoError(t, err)
			c.validate(t, manifests)
		})
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

func newTestAddOnWithInstallNamespace(name, namespace, installNamespace string) *addonv1alpha1.ManagedClusterAddOn {
	addon := &addonv1alpha1.ManagedClusterAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: addonv1alpha1.ManagedClusterAddOnSpec{
			InstallNamespace: name,
		},
	}
	addon.Spec.InstallNamespace = installNamespace
	return addon
}
