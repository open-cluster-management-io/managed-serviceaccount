package manager

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
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

// TestSetupPermission_tokenDriver_bindsRegistrationSubject documents GitHub issue #279: with klusterlet token
// registration, the hub RoleBinding must use the subject published in ManagedClusterAddOn status (the
// system:serviceaccount:... identity), not the CSR-style OCM user.
func TestSetupPermission_tokenDriver_bindsRegistrationSubject(t *testing.T) {
	clusterName := "cluster1"
	addonName := common.AddonName
	expectedSubjectUser := "system:serviceaccount:" + clusterName + ":" + addonName + "-agent"

	fakeKubeClient := fakekube.NewSimpleClientset()
	permissionConfig := NewRegistrationOption(fakeKubeClient).PermissionConfig

	addon := newTestAddOn(addonName, clusterName)
	addon.Status.KubeClientDriver = "token"
	addon.Status.Registrations = []addonv1alpha1.RegistrationConfig{
		{
			SignerName: certificatesv1.KubeAPIServerClientSignerName,
			Subject: addonv1alpha1.Subject{
				User: expectedSubjectUser,
			},
		},
	}

	err := permissionConfig(newTestCluster(clusterName), addon)
	assert.NoError(t, err)

	var roleBinding *rbacv1.RoleBinding
	for _, a := range fakeKubeClient.Actions() {
		create, ok := a.(clienttesting.CreateAction)
		if !ok {
			continue
		}
		if rb, ok := create.GetObject().(*rbacv1.RoleBinding); ok {
			roleBinding = rb
			break
		}
	}
	assert.NotNil(t, roleBinding, "expected a RoleBinding create action")
	if roleBinding == nil {
		return
	}
	assert.Len(t, roleBinding.Subjects, 1, "expected exactly one subject on RoleBinding")
	assert.Equal(t, rbacv1.UserKind, roleBinding.Subjects[0].Kind)
	assert.Equal(t, expectedSubjectUser, roleBinding.Subjects[0].Name,
		"token registration must bind the hub kube client subject user, not the CSR registration user")
}

// TestSetupPermission_tokenDriver_subjectNotReady verifies we wait until the spoke has published
// registration subject before applying hub RBAC.
func TestSetupPermission_tokenDriver_subjectNotReady(t *testing.T) {
	clusterName := "cluster1"
	addonName := common.AddonName

	fakeKubeClient := fakekube.NewSimpleClientset()
	permissionConfig := NewRegistrationOption(fakeKubeClient).PermissionConfig

	addon := newTestAddOn(addonName, clusterName)
	addon.Status.KubeClientDriver = "token"
	// Agent has not yet populated status.registrations[].subject

	err := permissionConfig(newTestCluster(clusterName), addon)
	var subjectErr *agent.SubjectNotReadyError
	assert.True(t, errors.As(err, &subjectErr), "expected SubjectNotReadyError while token subject is unset, got %v", err)

	// No RBAC should be applied until subject is known
	for _, a := range fakeKubeClient.Actions() {
		if _, ok := a.(clienttesting.CreateAction); ok {
			t.Fatalf("expected no API writes before registration subject is ready, saw action %#v", a)
		}
	}
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
