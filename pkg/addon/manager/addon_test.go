package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakekube "k8s.io/client-go/kubernetes/fake"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

func TestNewRegistrationOption(t *testing.T) {
	clusterName := "cluster1"
	fakeKubeClient := fakekube.NewSimpleClientset()
	addon := newTestAddOn(common.AddonName, clusterName)
	addon.Status.Registrations = []addonv1beta1.RegistrationConfig{
		newKubeClientRegistration(
			"csr",
			agent.DefaultUser(clusterName, common.AddonName, common.AgentName),
			agent.DefaultGroups(clusterName, common.AddonName),
		),
	}

	registrationOptions := NewRegistrationOption(fakeKubeClient)
	assert.NotNil(t, registrationOptions.PermissionConfig, "permissionConfig is not specified")

	err := registrationOptions.PermissionConfig(context.Background(), newTestCluster(clusterName), addon)
	assert.NoError(t, err)

	role, err := fakeKubeClient.RbacV1().Roles(clusterName).Get(context.Background(), permissionName, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, clusterName, role.Namespace, "invalid role ns")
	assert.Equal(t, permissionName, role.Name, "invalid role name")
	rolebinding := getRoleBinding(t, fakeKubeClient, clusterName)
	assert.Equal(t, clusterName, rolebinding.Namespace, "invalid rolebinding ns")
	assert.Equal(t, permissionName, rolebinding.Name, "invalid rolebinding name")
}

func TestSetupPermission(t *testing.T) {
	clusterName := "cluster1"
	tokenUser := "system:serviceaccount:" + clusterName + ":" + common.AddonName + "-agent"
	addonGroup := "system:open-cluster-management:cluster:" + clusterName + ":addon:" + common.AddonName
	defaultUser := agent.DefaultUser(clusterName, common.AddonName, common.AgentName)
	defaultGroups := agent.DefaultGroups(clusterName, common.AddonName)

	cases := []struct {
		name          string
		registrations []addonv1beta1.RegistrationConfig
		wantSubjects  []rbacv1.Subject
		wantNotReady  bool
	}{
		{
			name: "token driver binds registration subject and filters system:authenticated",
			registrations: []addonv1beta1.RegistrationConfig{
				newKubeClientRegistration("token", tokenUser, []string{addonGroup, "system:authenticated"}),
			},
			wantSubjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: tokenUser},
				{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: addonGroup},
			},
		},
		{
			name: "csr driver binds registration subject",
			registrations: []addonv1beta1.RegistrationConfig{
				newKubeClientRegistration("csr", defaultUser, defaultGroups),
			},
			wantSubjects: newRBACSubjects(defaultUser, defaultGroups),
		},
		{
			name: "empty driver binds registration subject",
			registrations: []addonv1beta1.RegistrationConfig{
				newKubeClientRegistration("", defaultUser, defaultGroups),
			},
			wantSubjects: newRBACSubjects(defaultUser, defaultGroups),
		},
		{
			name: "empty registration subject is not ready",
			registrations: []addonv1beta1.RegistrationConfig{
				newKubeClientRegistration("token", "", nil),
			},
			wantNotReady: true,
		},
		{
			name:         "missing registration subject is not ready",
			wantNotReady: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeKubeClient := fakekube.NewSimpleClientset()
			addon := newTestAddOn(common.AddonName, clusterName)
			addon.Status.Registrations = c.registrations

			err := NewRegistrationOption(fakeKubeClient).PermissionConfig(context.Background(), newTestCluster(clusterName), addon)
			if c.wantNotReady {
				var subjectErr *agent.SubjectNotReadyError
				assert.ErrorAs(t, err, &subjectErr)
				_, roleErr := fakeKubeClient.RbacV1().Roles(clusterName).Get(context.Background(), permissionName, metav1.GetOptions{})
				assert.NoError(t, roleErr)
				_, roleBindingErr := fakeKubeClient.RbacV1().RoleBindings(clusterName).Get(context.Background(), permissionName, metav1.GetOptions{})
				assert.True(t, apierrors.IsNotFound(roleBindingErr), "expected rolebinding not found, got %v", roleBindingErr)
				return
			}
			assert.NoError(t, err)
			roleBinding := getRoleBinding(t, fakeKubeClient, clusterName)
			assert.Equal(t, c.wantSubjects, roleBinding.Subjects)
		})
	}
}

func newRBACSubjects(user string, groups []string) []rbacv1.Subject {
	subjects := []rbacv1.Subject{
		{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user},
	}
	for _, group := range groups {
		subjects = append(subjects, rbacv1.Subject{
			Kind:     rbacv1.GroupKind,
			APIGroup: rbacv1.GroupName,
			Name:     group,
		})
	}
	return subjects
}

func getRoleBinding(t *testing.T, client *fakekube.Clientset, namespace string) *rbacv1.RoleBinding {
	t.Helper()
	roleBinding, err := client.RbacV1().RoleBindings(namespace).Get(context.Background(), permissionName, metav1.GetOptions{})
	assert.NoError(t, err)
	return roleBinding
}

func newKubeClientRegistration(driver, user string, groups []string) addonv1beta1.RegistrationConfig {
	return addonv1beta1.RegistrationConfig{
		Type: addonv1beta1.KubeClient,
		KubeClient: &addonv1beta1.KubeClientConfig{
			Driver: driver,
			Subject: addonv1beta1.KubeClientSubject{
				BaseSubject: addonv1beta1.BaseSubject{
					User:   user,
					Groups: groups,
				},
			},
		},
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

			manifests, err := addOnAgent.Manifests(context.Background(), newTestCluster(clusterName), newTestAddOn(addonName, clusterName))
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

func newTestAddOn(name, namespace string) *addonv1beta1.ManagedClusterAddOn {
	return &addonv1beta1.ManagedClusterAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{addonv1beta1.InstallNamespaceAnnotation: name},
		},
	}
}
