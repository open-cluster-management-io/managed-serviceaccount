package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	"open-cluster-management.io/addon-framework/pkg/utils"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	fakeaddon "open-cluster-management.io/api/client/addon/clientset/versioned/fake"
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
			manifests := renderTestManifests(
				t,
				newTestCluster(clusterName),
				newTestAddOn(addonName, clusterName),
				c.getValuesFunc...,
			)

			actual := []string{}
			var agentDeployment *appsv1.Deployment
			for _, manifest := range manifests {
				obj, ok := manifest.(metav1.ObjectMetaAccessor)
				assert.True(t, ok, "invalid manifest")
				if ns := obj.GetObjectMeta().GetNamespace(); len(ns) > 0 {
					assert.Equalf(t, addonName, ns, "unexpected ns of manifest %q", obj.GetObjectMeta().GetName())
				}
				actual = append(actual, obj.GetObjectMeta().GetName())
				if deployment, ok := manifest.(*appsv1.Deployment); ok {
					agentDeployment = deployment
				}
			}
			assert.ElementsMatch(t, c.expectedManifestNames, actual)
			if assert.NotNil(t, agentDeployment, "expected addon agent Deployment manifest") {
				assertAgentSecurityContext(t, agentDeployment)
			}
		})
	}
}

func assertAgentSecurityContext(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()

	podSpec := deployment.Spec.Template.Spec
	assert.Equal(t, &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}, podSpec.SecurityContext)

	if !assert.Len(t, podSpec.Containers, 1, "expected one addon agent container") {
		return
	}
	assert.Equal(t, &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}, podSpec.Containers[0].SecurityContext)
}

func TestManifestAddonServiceMonitor(t *testing.T) {
	clusterName := "cluster1"
	cases := []struct {
		name           string
		variables      []addonv1beta1.CustomizedVariable
		expectMonitor  bool
		expectedLabels map[string]string
	}{
		{
			name:          "disabled by default",
			expectMonitor: false,
		},
		{
			name: "enabled without labels",
			variables: []addonv1beta1.CustomizedVariable{
				{Name: prometheusEnabledVariableName, Value: "true"},
			},
			expectMonitor: true,
		},
		{
			name: "labels from AddOnDeploymentConfig",
			variables: []addonv1beta1.CustomizedVariable{
				{Name: prometheusEnabledVariableName, Value: "true"},
				{Name: prometheusServiceMonitorLabelsVariableName, Value: `{"release":"prometheus","team":"platform"}`},
			},
			expectMonitor:  true,
			expectedLabels: map[string]string{"release": "prometheus", "team": "platform"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			manifests := renderWithConfig(t, clusterName, "addon1", "imageName1", c.variables...)

			var serviceMonitor *unstructured.Unstructured
			for _, manifest := range manifests {
				if object, ok := manifest.(*unstructured.Unstructured); ok && object.GetKind() == "ServiceMonitor" {
					serviceMonitor = object
				}
			}
			if !c.expectMonitor {
				assert.Nil(t, serviceMonitor, "ServiceMonitor should not be rendered when Prometheus is disabled")
				return
			}
			if assert.NotNil(t, serviceMonitor, "servicemonitor not found") {
				assert.Equal(t, "managed-serviceaccount-addon-agent", serviceMonitor.GetName())
				assert.Equal(t, c.expectedLabels, serviceMonitor.GetLabels())
			}
		})
	}
}

func TestToAddOnPrometheusValuesRejectsInvalidServiceMonitorLabels(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "invalid json",
			raw:  "not-json",
		},
		{
			name: "non string label value",
			raw:  `{"release":1}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ToAddOnPrometheusValues(addonv1beta1.AddOnDeploymentConfig{
				Spec: addonv1beta1.AddOnDeploymentConfigSpec{
					CustomizedVariables: []addonv1beta1.CustomizedVariable{
						{Name: prometheusServiceMonitorLabelsVariableName, Value: c.raw},
					},
				},
			})
			assert.Error(t, err)
		})
	}
}

func renderTestManifests(
	t *testing.T,
	cluster *clusterv1.ManagedCluster,
	addon *addonv1beta1.ManagedClusterAddOn,
	getValuesFuncs ...addonfactory.GetValuesFunc,
) []runtime.Object {
	t.Helper()

	agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, FS, "manifests/templates").
		WithScheme(NewAgentScheme()).
		WithGetValuesFuncs(getValuesFuncs...)

	addOnAgent, err := agentFactory.BuildTemplateAgentAddon()
	assert.NoError(t, err)

	manifests, err := addOnAgent.Manifests(context.Background(), cluster, addon)
	assert.NoError(t, err)

	return manifests
}

func renderWithConfig(t *testing.T, clusterName, addonName, imageName string, variables ...addonv1beta1.CustomizedVariable) []runtime.Object {
	t.Helper()
	config := &addonv1beta1.AddOnDeploymentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metrics-config",
			Namespace: clusterName,
		},
		Spec: addonv1beta1.AddOnDeploymentConfigSpec{
			CustomizedVariables: variables,
		},
	}
	addon := newTestAddOn(addonName, clusterName)
	addon.Status.ConfigReferences = []addonv1beta1.ConfigReference{
		{
			ConfigGroupResource: addonv1beta1.ConfigGroupResource{
				Group:    utils.AddOnDeploymentConfigGVR.Group,
				Resource: utils.AddOnDeploymentConfigGVR.Resource,
			},
			DesiredConfig: &addonv1beta1.ConfigSpecHash{
				ConfigReferent: addonv1beta1.ConfigReferent{
					Namespace: config.Namespace,
					Name:      config.Name,
				},
				SpecHash: "hash",
			},
		},
	}

	return renderTestManifests(
		t,
		newTestCluster(clusterName),
		addon,
		GetDefaultValues(imageName, nil),
		addonfactory.GetAddOnDeploymentConfigValues(
			utils.NewAddOnDeploymentConfigGetter(fakeaddon.NewSimpleClientset(config)),
			addonfactory.ToAddOnDeploymentConfigValues,
			ToAddOnPrometheusValues,
		),
	)
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
