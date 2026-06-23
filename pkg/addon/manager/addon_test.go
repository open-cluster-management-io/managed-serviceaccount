package manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/provisioner"
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
	hubKubeconfigSecretName := "hub-kubeconfig-secret"
	installNamespace := addonfactory.AddonDefaultInstallNamespace
	manifestNames := []string{
		installNamespace,
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
		expectedImage         string
		expectedNodeSelector  map[string]string
		expectedTolerations   []corev1.Toleration
	}{
		{
			name:                  "install",
			getValuesFunc:         []addonfactory.GetValuesFunc{GetDefaultValues(imageName, nil)},
			expectedManifestNames: manifestNames,
			expectedImage:         imageName,
		},
		{
			name:                  "install all with image pull secret",
			getValuesFunc:         []addonfactory.GetValuesFunc{GetDefaultValues(imageName, newTestImagePullSecret())},
			expectedManifestNames: append(manifestNames, "open-cluster-management-image-pull-credentials"),
			expectedImage:         imageName,
		},
		{
			name: "node placement is rendered on the agent deployment",
			getValuesFunc: []addonfactory.GetValuesFunc{
				GetDefaultValues(imageName, nil),
				getNodePlacementValues(
					map[string]string{"kubernetes.io/os": "linux"},
					[]corev1.Toleration{{Key: "foo", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}},
				),
			},
			expectedManifestNames: manifestNames,
			expectedImage:         imageName,
			expectedNodeSelector:  map[string]string{"kubernetes.io/os": "linux"},
			expectedTolerations:   []corev1.Toleration{{Key: "foo", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getValuesFuncs := append([]addonfactory.GetValuesFunc{}, c.getValuesFunc...)
			getValuesFuncs = append(getValuesFuncs, getHubKubeconfigSecretValues(hubKubeconfigSecretName))
			manifests := renderTestManifests(
				t,
				newTestCluster(clusterName),
				newTestAddOn(addonName, clusterName),
				getValuesFuncs...,
			)

			actual := []string{}
			var agentDeployment *appsv1.Deployment
			for _, manifest := range manifests {
				obj, ok := manifest.(metav1.ObjectMetaAccessor)
				assert.True(t, ok, "invalid manifest")
				if ns := obj.GetObjectMeta().GetNamespace(); len(ns) > 0 {
					assert.Equalf(t, installNamespace, ns, "unexpected ns of manifest %q", obj.GetObjectMeta().GetName())
				}
				actual = append(actual, obj.GetObjectMeta().GetName())
				if deployment, ok := manifest.(*appsv1.Deployment); ok {
					agentDeployment = deployment
				}
			}
			assert.ElementsMatch(t, c.expectedManifestNames, actual)
			if assert.NotNil(t, agentDeployment, "expected addon agent Deployment manifest") {
				assertAgentSecurityContext(t, agentDeployment)
				if !assert.NotEmpty(t, agentDeployment.Spec.Template.Spec.Containers, "expected at least one container") {
					return
				}
				container := agentDeployment.Spec.Template.Spec.Containers[0]
				assert.Equal(t, c.expectedImage, container.Image)
				assert.Contains(t, container.Args, "--cluster-name="+clusterName)
				assert.Equal(t, c.expectedNodeSelector, agentDeployment.Spec.Template.Spec.NodeSelector)
				assert.Equal(t, c.expectedTolerations, agentDeployment.Spec.Template.Spec.Tolerations)
				assertDeploymentSecretVolume(t, agentDeployment, "hub-kubeconfig", hubKubeconfigSecretName)
			}
		})
	}
}

func getHubKubeconfigSecretValues(secretName string) addonfactory.GetValuesFunc {
	return func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
		return addonfactory.Values{"hubKubeConfigSecret": secretName}, nil
	}
}

func getNodePlacementValues(nodeSelector map[string]string, tolerations []corev1.Toleration) addonfactory.GetValuesFunc {
	return func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
		return addonfactory.ToAddOnDeploymentConfigValues(addonv1beta1.AddOnDeploymentConfig{
			Spec: addonv1beta1.AddOnDeploymentConfigSpec{
				NodePlacement: &addonv1beta1.NodePlacement{
					NodeSelector: nodeSelector,
					Tolerations:  tolerations,
				},
			},
		})
	}
}

func TestGetDefaultValuesRequiresDockerConfigJsonKey(t *testing.T) {
	cases := []struct {
		name string
		data map[string][]byte
	}{
		{
			name: "missing docker config key",
			data: map[string][]byte{},
		},
		{
			name: "empty docker config",
			data: map[string][]byte{
				corev1.DockerConfigJsonKey: {},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			imagePullSecret := newTestImagePullSecret()
			imagePullSecret.Data = c.data

			values, err := GetDefaultValues("imageName1", imagePullSecret)(newTestCluster("cluster1"), newTestAddOn(common.AddonName, "cluster1"))

			assert.Nil(t, values)
			assert.ErrorContains(t, err, `missing ".dockerconfigjson"`)
		})
	}
}

func TestManifestAddonAgentUsesDeploymentConfigInstallNamespace(t *testing.T) {
	clusterName := "cluster1"
	installNamespace := "custom-agent-namespace"
	config := newTestAddOnDeploymentConfig(clusterName, "install-namespace-config", installNamespace)
	addon := newTestAddOn(common.AddonName, clusterName)
	addon.Status.ConfigReferences = newTestConfigReferences(config)
	fakeAddonClient := fakeaddon.NewSimpleClientset(config)
	deploymentConfigGetter := utils.NewAddOnDeploymentConfigGetter(fakeAddonClient)

	manifests := renderTestManifestsWithNamespaceFunc(
		t,
		newTestCluster(clusterName),
		addon,
		utils.AgentInstallNamespaceFromDeploymentConfigFunc(deploymentConfigGetter),
		false,
		GetDefaultValues("imageName1", nil),
		addonfactory.GetAddOnDeploymentConfigValues(
			deploymentConfigGetter,
			addonfactory.ToAddOnDeploymentConfigValues,
		),
	)

	var agentDeployment *appsv1.Deployment
	for _, manifest := range manifests {
		obj, ok := manifest.(metav1.ObjectMetaAccessor)
		assert.True(t, ok, "invalid manifest")
		if namespace, ok := manifest.(*corev1.Namespace); ok {
			assert.Equal(t, installNamespace, namespace.Name)
			continue
		}
		if ns := obj.GetObjectMeta().GetNamespace(); len(ns) > 0 {
			assert.Equalf(t, installNamespace, ns, "unexpected ns of manifest %q", obj.GetObjectMeta().GetName())
		}
		if deployment, ok := manifest.(*appsv1.Deployment); ok {
			agentDeployment = deployment
		}
	}
	if assert.NotNil(t, agentDeployment, "expected addon agent Deployment manifest") {
		assert.Equal(t, installNamespace, agentDeployment.Namespace)
	}
}

func TestManifestAddonAgentRequiresDeploymentConfigSpecHash(t *testing.T) {
	clusterName := "cluster1"
	config := newTestAddOnDeploymentConfig(clusterName, "install-namespace-config", "custom-agent-namespace")
	addon := newTestAddOn(common.AddonName, clusterName)
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
			},
		},
	}
	fakeAddonClient := fakeaddon.NewSimpleClientset(config)
	deploymentConfigGetter := utils.NewAddOnDeploymentConfigGetter(fakeAddonClient)

	agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, FS, "manifests/charts/managed-serviceaccount-agent").
		WithScheme(NewAgentScheme()).
		WithAgentInstallNamespace(utils.AgentInstallNamespaceFromDeploymentConfigFunc(deploymentConfigGetter)).
		WithGetValuesFuncs(
			GetDefaultValues("imageName1", nil),
			addonfactory.GetAddOnDeploymentConfigValues(
				deploymentConfigGetter,
				addonfactory.ToAddOnDeploymentConfigValues,
			),
		)

	addOnAgent, err := agentFactory.BuildHelmAgentAddon()
	assert.NoError(t, err)

	_, err = addOnAgent.Manifests(context.Background(), newTestCluster(clusterName), addon)
	assert.ErrorContains(t, err, "deployment config desired spec hash is empty")
}

func assertAgentSecurityContext(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()

	podSpec := deployment.Spec.Template.Spec
	assertPodSecurityContext(t, podSpec)

	if !assert.Len(t, podSpec.Containers, 1, "expected one addon agent container") {
		return
	}
	assertContainerSecurityContext(t, podSpec.Containers[0])
}

func assertPodSecurityContext(t *testing.T, podSpec corev1.PodSpec) {
	t.Helper()

	assert.Equal(t, &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}, podSpec.SecurityContext)
}

func assertContainerSecurityContext(t *testing.T, container corev1.Container) {
	t.Helper()

	assert.Equal(t, &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}, container.SecurityContext)
}

func TestManifestAddonAgentDefaultModeDeployment(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		newTestAddOn(addonName, clusterName),
		true,
		GetDefaultValues(imageName, nil),
	)
	deployment := findDeployment(t, manifests)
	container := deployment.Spec.Template.Spec.Containers[0]

	assert.NotContains(t, deployment.Annotations, addonv1beta1.HostedManifestLocationAnnotationKey)
	assert.Contains(t, container.Args, "--leader-elect=false")
	assert.Contains(t, container.Args, "--cluster-name="+clusterName)
	assert.Contains(t, container.Args, "--install-mode=Default")
	assert.Contains(t, container.Args, "--kubeconfig=/etc/hub/kubeconfig")
	assert.Contains(t, container.Args, "--lease-health-check=true")
	assert.NotContains(t, container.Args, "--spoke-kubeconfig=/etc/managed/kubeconfig")
	assertDeploymentSecretVolume(t, deployment, "hub-kubeconfig", "managed-serviceaccount-hub-kubeconfig")
	assertDeploymentMissingVolume(t, deployment, "managed-kubeconfig")

	role := findRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assertRule(t, role.Rules, []string{"coordination.k8s.io"}, []string{"leases"}, []string{"get", "create", "update", "patch"}, nil)
	assertHostedManifestMissing[*rbacv1.Role](t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue, "Role")
}

func TestManifestAddonAgentHostedModeDeployment(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
	)
	deployment := findDeployment(t, manifests)
	container := deployment.Spec.Template.Spec.Containers[0]

	assert.Equal(t,
		addonv1beta1.HostedManifestLocationHostingValue,
		deployment.Annotations[addonv1beta1.HostedManifestLocationAnnotationKey])
	assert.Contains(t, container.Args, "--install-mode=Hosted")
	assert.Contains(t, container.Args, "--kubeconfig=/etc/hub/kubeconfig")
	assert.Contains(t, container.Args, "--spoke-kubeconfig=/etc/managed/kubeconfig")
	assert.Contains(t, container.Args, "--lease-health-check=true")
	assertDeploymentSecretVolume(t, deployment, "hub-kubeconfig", "managed-serviceaccount-hub-kubeconfig")
	assertDeploymentSecretVolume(t, deployment, "managed-kubeconfig", addonName+"-managed-kubeconfig")
	assertDeploymentVolumeMount(t, deployment, "hub-kubeconfig", "/etc/hub/")
	assertDeploymentVolumeMount(t, deployment, "managed-kubeconfig", "/etc/managed/")
}

func TestManifestAddonAgentHostedModeManagedKubeConfigSecretOverride(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	managedKubeConfigSecret := "custom-managed-kubeconfig"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"ManagedKubeConfigSecret": managedKubeConfigSecret,
			}, nil
		},
	)
	deployment := findDeployment(t, manifests)

	assertDeploymentSecretVolume(t, deployment, "managed-kubeconfig", managedKubeConfigSecret)
}

func TestManifestAddonAgentHostedModeManifestLocations(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, newTestImagePullSecret()),
	)

	assert.Equal(t, "", hostedLocation(findServiceAccount(t, manifests, "managed-serviceaccount", "")))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findServiceAccount(t, manifests, "managed-serviceaccount", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, "", hostedLocation(findRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")))
	assert.Equal(t, "", hostedLocation(findRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")))
	assertHostedManifestMissing[*rbacv1.Role](
		t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent",
		addonv1beta1.HostedManifestLocationHostingValue, "role")
	assertHostedManifestMissing[*rbacv1.RoleBinding](
		t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent",
		addonv1beta1.HostedManifestLocationHostingValue, "rolebinding")

	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue, hostedLocation(findDeployment(t, manifests)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findSecret(t, manifests, "open-cluster-management-image-pull-credentials", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findServiceAccount(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findRole(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findRoleBinding(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findRoleBinding(t, manifests, "managed-serviceaccount-kubeconfig-provisioner-source", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findRole(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findRoleBinding(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)))

	assert.Equal(t, addonv1beta1.HostedManifestLocationManagedValue,
		hostedLocation(findClusterRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent")))
	assert.Equal(t, addonv1beta1.HostedManifestLocationManagedValue,
		hostedLocation(findClusterRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent")))
}

func TestManifestAddonAgentHostedModeLeaseRBAC(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
	)

	deployment := findDeployment(t, manifests)
	assert.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, "--lease-health-check=true")
	assert.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, "--install-mode=Hosted")

	role := findRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assert.Equal(t, addonName, role.Namespace)
	assert.NotContains(t, role.Rules, rbacv1.PolicyRule{
		APIGroups: []string{"coordination.k8s.io"},
		Resources: []string{"leases"},
		Verbs:     []string{"get", "create", "update", "patch"},
	}, "hosted mode should grant lease permissions only on the hosting cluster")

	binding := findRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assert.Equal(t, addonName, binding.Namespace)
	assert.Equal(t, "Role", binding.RoleRef.Kind)
	assert.Equal(t, "open-cluster-management:managed-serviceaccount:addon-agent", binding.RoleRef.Name)
	assert.Len(t, binding.Subjects, 1)
	assert.Equal(t, "ServiceAccount", binding.Subjects[0].Kind)
	assert.Equal(t, provisioner.DefaultManagedServiceAccountName, binding.Subjects[0].Name)
	assert.Equal(t, addonName, binding.Subjects[0].Namespace)

	hostingRole := findRole(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, hostingRole.Namespace)
	assertRule(t, hostingRole.Rules, []string{"coordination.k8s.io"}, []string{"leases"}, []string{"get", "create", "update", "patch"}, nil)

	hostingBinding := findRoleBinding(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, hostingBinding.Namespace)
	assert.Equal(t, "Role", hostingBinding.RoleRef.Kind)
	assert.Equal(t, "managed-serviceaccount-health-lease", hostingBinding.RoleRef.Name)
	assert.Len(t, hostingBinding.Subjects, 1)
	assert.Equal(t, "ServiceAccount", hostingBinding.Subjects[0].Kind)
	assert.Equal(t, "managed-serviceaccount", hostingBinding.Subjects[0].Name)
	assert.Equal(t, addonName, hostingBinding.Subjects[0].Namespace)
}

func TestManifestAddonAgentHostedModeExternalManagedKubeConfigOverrides(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"ExternalManagedKubeConfigNamespace": "custom-source-ns",
				"ExternalManagedKubeConfigSecret":    "custom-source-secret",
				"ManagedKubeConfigSecret":            "custom-target-secret",
			}, nil
		},
	)

	provisioner := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	args := provisioner.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--source-namespace=custom-source-ns")
	assert.Contains(t, args, "--source-secret=custom-source-secret")
	assert.Contains(t, args, "--target-secret=custom-target-secret")
	assert.Contains(t, args, "--hub-kubeconfig-secret=managed-serviceaccount-hub-kubeconfig")

	targetRole := findRole(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, targetRole.Namespace)
	assertRule(t, targetRole.Rules, []string{""}, []string{"secrets"}, []string{"get", "update", "patch", "delete"}, []string{"custom-target-secret", "managed-serviceaccount-hub-kubeconfig"})
	assertRule(t, targetRole.Rules, []string{""}, []string{"secrets"}, []string{"create"}, nil)

	sourceRole := findRole(t, manifests, "managed-serviceaccount-kubeconfig-provisioner-source", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, "custom-source-ns", sourceRole.Namespace)
	assertRule(t, sourceRole.Rules, []string{""}, []string{"secrets"}, []string{"get"}, []string{"custom-source-secret"})

	sourceBinding := findRoleBinding(t, manifests, "managed-serviceaccount-kubeconfig-provisioner-source", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, "custom-source-ns", sourceBinding.Namespace)
	assert.Len(t, sourceBinding.Subjects, 1)
	assert.Equal(t, "managed-serviceaccount-kubeconfig-provisioner", sourceBinding.Subjects[0].Name)
	assert.Equal(t, addonName, sourceBinding.Subjects[0].Namespace)
}

func TestManifestAddonAgentHostedModeProvisionerTimingDefaults(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
	)

	prov := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	args := prov.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--source-namespace="+clusterName)
	assert.Contains(t, args, "--source-secret=external-managed-kubeconfig")
	assert.Contains(t, args, "--target-secret="+addonName+"-managed-kubeconfig")
	assert.Contains(t, args, "--hub-kubeconfig-secret=managed-serviceaccount-hub-kubeconfig")
	assert.Contains(t, args, fmt.Sprintf("--token-expiration-seconds=%d", provisioner.DefaultTokenExpirationSeconds))
	assert.Contains(t, args, fmt.Sprintf("--refresh-before=%s", provisioner.DefaultRefreshBefore))
	assert.Contains(t, args, fmt.Sprintf("--sync-interval=%s", provisioner.DefaultSyncInterval))

	sourceRole := findRole(t, manifests, "managed-serviceaccount-kubeconfig-provisioner-source", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, clusterName, sourceRole.Namespace)
	assertRule(t, sourceRole.Rules, []string{""}, []string{"secrets"}, []string{"get"}, []string{"external-managed-kubeconfig"})

	sourceBinding := findRoleBinding(t, manifests, "managed-serviceaccount-kubeconfig-provisioner-source", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, clusterName, sourceBinding.Namespace)
	assert.Len(t, sourceBinding.Subjects, 1)
	assert.Equal(t, "managed-serviceaccount-kubeconfig-provisioner", sourceBinding.Subjects[0].Name)
	assert.Equal(t, addonName, sourceBinding.Subjects[0].Namespace)
}

func TestManifestAddonAgentHostedModeProvisionerTimingOverrides(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"ManagedKubeConfigTokenExpirationSeconds":  int64(7200),
				"ManagedKubeConfigRefreshBefore":           "15m",
				"ManagedKubeConfigProvisionerSyncInterval": "30s",
			}, nil
		},
	)

	prov := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	args := prov.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--token-expiration-seconds=7200")
	assert.Contains(t, args, "--refresh-before=15m")
	assert.Contains(t, args, "--sync-interval=30s")

	assert.NotContains(t, args, fmt.Sprintf("--token-expiration-seconds=%d", provisioner.DefaultTokenExpirationSeconds))
	assert.NotContains(t, args, fmt.Sprintf("--refresh-before=%s", provisioner.DefaultRefreshBefore))
	assert.NotContains(t, args, fmt.Sprintf("--sync-interval=%s", provisioner.DefaultSyncInterval))
}

func TestManifestAddonAgentHostedModeProvisionerSecurityContext(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
	)

	provisioner := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	assertPodSecurityContext(t, provisioner.Spec.Template.Spec)
	if assert.Len(t, provisioner.Spec.Template.Spec.Containers, 1, "expected one provisioner container") {
		assertContainerSecurityContext(t, provisioner.Spec.Template.Spec.Containers[0])
	}

	cleanup := findJob(t, manifests, "managed-serviceaccount-kubeconfig-cleanup")
	assertPodSecurityContext(t, cleanup.Spec.Template.Spec)
	if assert.Len(t, cleanup.Spec.Template.Spec.Containers, 1, "expected one cleanup container") {
		assertContainerSecurityContext(t, cleanup.Spec.Template.Spec.Containers[0])
	}
}

func TestManifestAddonAgentHostedModeProvisionerNodePlacement(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")
	nodeSelector := map[string]string{"node-role.kubernetes.io/hosting": "true"}
	tolerations := []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "hosting", Effect: corev1.TaintEffectNoSchedule}}

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
		getNodePlacementValues(nodeSelector, tolerations),
	)

	provisioner := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	assert.Equal(t, nodeSelector, provisioner.Spec.Template.Spec.NodeSelector)
	assert.Equal(t, tolerations, provisioner.Spec.Template.Spec.Tolerations)

	cleanup := findJob(t, manifests, "managed-serviceaccount-kubeconfig-cleanup")
	assert.Equal(t, nodeSelector, cleanup.Spec.Template.Spec.NodeSelector)
	assert.Equal(t, tolerations, cleanup.Spec.Template.Spec.Tolerations)
}

func TestManifestAddonAgentDefaultModeManagedServiceAccountNameOverride(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	customName := "custom-msa"
	installNamespace := addonfactory.AddonDefaultInstallNamespace

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		newTestAddOn(addonName, clusterName),
		false,
		GetDefaultValues(imageName, nil),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"ManagedServiceAccountName": customName,
			}, nil
		},
	)

	sa := findServiceAccount(t, manifests, customName, "")
	assert.Equal(t, installNamespace, sa.Namespace)

	binding := findRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assert.Len(t, binding.Subjects, 1)
	assert.Equal(t, customName, binding.Subjects[0].Name)
	assert.Equal(t, installNamespace, binding.Subjects[0].Namespace)

	clusterBinding := findClusterRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent")
	assert.Len(t, clusterBinding.Subjects, 1)
	assert.Equal(t, customName, clusterBinding.Subjects[0].Name)
	assert.Equal(t, installNamespace, clusterBinding.Subjects[0].Namespace)

	deployment := findDeployment(t, manifests)
	assert.Equal(t, customName, deployment.Spec.Template.Spec.ServiceAccountName)
}

func TestManifestAddonAgentHostedModeManagedServiceAccountNameOverride(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	customName := "custom-msa"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"ManagedServiceAccountName": customName,
			}, nil
		},
	)

	sa := findServiceAccount(t, manifests, customName, "")
	assert.Equal(t, addonName, sa.Namespace)

	binding := findRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assert.Len(t, binding.Subjects, 1)
	assert.Equal(t, customName, binding.Subjects[0].Name)
	assert.Equal(t, addonName, binding.Subjects[0].Namespace)

	clusterBinding := findClusterRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent")
	assert.Len(t, clusterBinding.Subjects, 1)
	assert.Equal(t, customName, clusterBinding.Subjects[0].Name)
	assert.Equal(t, addonName, clusterBinding.Subjects[0].Namespace)

	provisioner := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	assert.Contains(t, provisioner.Spec.Template.Spec.Containers[0].Args, "--managed-serviceaccount-name="+customName)

	deployment := findDeployment(t, manifests)
	assert.Equal(t, "managed-serviceaccount", deployment.Spec.Template.Spec.ServiceAccountName)
	hostingSA := findServiceAccount(t, manifests, "managed-serviceaccount", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, hostingSA.Namespace)
}

func TestManifestAddonAgentHostedModeCleanupHook(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
	)

	job := findJob(t, manifests, "managed-serviceaccount-kubeconfig-cleanup")
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue, hostedLocation(job))
	assert.Contains(t, job.Annotations, addonv1beta1.AddonPreDeleteHookAnnotationKey)
	args := job.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--cleanup")
	assert.Contains(t, args, "--source-namespace="+clusterName)
	assert.Contains(t, args, "--source-secret=external-managed-kubeconfig")
	assert.Contains(t, args, "--target-secret="+addonName+"-managed-kubeconfig")
	assert.Contains(t, args, "--hub-kubeconfig-secret=managed-serviceaccount-hub-kubeconfig")
}

func TestManifestAddonAgentHostedModeNamespaces(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	addon := newTestHostedAddOn(addonName, clusterName, "hosting1")

	manifests := renderTestManifestsWithHostedMode(
		t,
		newTestCluster(clusterName),
		addon,
		true,
		GetDefaultValues(imageName, nil),
	)
	namespaces := findNamespaces(manifests)

	assert.Len(t, namespaces, 2)
	hostingNamespaces := 0
	managedNamespaces := 0
	for _, namespace := range namespaces {
		assert.Equal(t, addonName, namespace.Name)
		if namespace.Annotations[addonv1beta1.HostedManifestLocationAnnotationKey] == addonv1beta1.HostedManifestLocationHostingValue {
			hostingNamespaces++
		} else {
			managedNamespaces++
		}
	}
	assert.Equal(t, 1, hostingNamespaces)
	assert.Equal(t, 1, managedNamespaces)
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
	return renderTestManifestsWithNamespaceFunc(t, cluster, addon, nil, false, getValuesFuncs...)
}

func renderTestManifestsWithHostedMode(
	t *testing.T,
	cluster *clusterv1.ManagedCluster,
	addon *addonv1beta1.ManagedClusterAddOn,
	hostedModeEnabled bool,
	getValuesFuncs ...addonfactory.GetValuesFunc,
) []runtime.Object {
	return renderTestManifestsWithNamespaceFunc(t, cluster, addon, nil, hostedModeEnabled, getValuesFuncs...)
}

func renderTestManifestsWithNamespaceFunc(
	t *testing.T,
	cluster *clusterv1.ManagedCluster,
	addon *addonv1beta1.ManagedClusterAddOn,
	agentInstallNamespace agent.AgentInstallNamespaceFunc,
	hostedModeEnabled bool,
	getValuesFuncs ...addonfactory.GetValuesFunc,
) []runtime.Object {
	t.Helper()

	agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, FS, "manifests/charts/managed-serviceaccount-agent").
		WithScheme(NewAgentScheme()).
		WithGetValuesFuncs(getValuesFuncs...).
		WithAgentRegistrationOption(NewRegistrationOption(fakekube.NewSimpleClientset()))
	if agentInstallNamespace != nil {
		agentFactory = agentFactory.WithAgentInstallNamespace(agentInstallNamespace)
	}
	if hostedModeEnabled {
		agentFactory = agentFactory.WithAgentHostedModeEnabledOption()
	}

	addOnAgent, err := agentFactory.BuildHelmAgentAddon()
	assert.NoError(t, err)

	manifests, err := addOnAgent.Manifests(context.Background(), cluster, addon)
	assert.NoError(t, err)

	return manifests
}

func renderWithConfig(t *testing.T, clusterName, addonName, imageName string, variables ...addonv1beta1.CustomizedVariable) []runtime.Object {
	t.Helper()
	config := newTestAddOnDeploymentConfig(clusterName, "metrics-config", addonName)
	config.Spec.CustomizedVariables = variables
	addon := newTestAddOn(addonName, clusterName)
	addon.Status.ConfigReferences = newTestConfigReferences(config)
	fakeAddonClient := fakeaddon.NewSimpleClientset(config)
	deploymentConfigGetter := utils.NewAddOnDeploymentConfigGetter(fakeAddonClient)

	return renderTestManifestsWithNamespaceFunc(
		t,
		newTestCluster(clusterName),
		addon,
		utils.AgentInstallNamespaceFromDeploymentConfigFunc(deploymentConfigGetter),
		false,
		GetDefaultValues(imageName, nil),
		addonfactory.GetAddOnDeploymentConfigValues(
			deploymentConfigGetter,
			addonfactory.ToAddOnDeploymentConfigValues,
			ToAddOnPrometheusValues,
		),
	)
}

func findDeployment(t *testing.T, manifests []runtime.Object) *appsv1.Deployment {
	t.Helper()
	return findDeploymentByName(t, manifests, "managed-serviceaccount-addon-agent")
}

func findDeploymentByName(t *testing.T, manifests []runtime.Object, name string) *appsv1.Deployment {
	t.Helper()
	return findManifestByName[*appsv1.Deployment](t, manifests, name, "deployment")
}

func findJob(t *testing.T, manifests []runtime.Object, name string) *batchv1.Job {
	t.Helper()
	return findManifestByName[*batchv1.Job](t, manifests, name, "job")
}

func findSecret(t *testing.T, manifests []runtime.Object, name, location string) *corev1.Secret {
	t.Helper()
	return findHostedManifest[*corev1.Secret](t, manifests, name, location, "secret")
}

func findServiceAccount(t *testing.T, manifests []runtime.Object, name, location string) *corev1.ServiceAccount {
	t.Helper()
	return findHostedManifest[*corev1.ServiceAccount](t, manifests, name, location, "serviceaccount")
}

func findRole(t *testing.T, manifests []runtime.Object, name, location string) *rbacv1.Role {
	t.Helper()
	return findHostedManifest[*rbacv1.Role](t, manifests, name, location, "role")
}

func findRoleBinding(t *testing.T, manifests []runtime.Object, name, location string) *rbacv1.RoleBinding {
	t.Helper()
	return findHostedManifest[*rbacv1.RoleBinding](t, manifests, name, location, "rolebinding")
}

func findClusterRole(t *testing.T, manifests []runtime.Object, name string) *rbacv1.ClusterRole {
	t.Helper()
	return findManifestByName[*rbacv1.ClusterRole](t, manifests, name, "clusterrole")
}

func findClusterRoleBinding(t *testing.T, manifests []runtime.Object, name string) *rbacv1.ClusterRoleBinding {
	t.Helper()
	return findManifestByName[*rbacv1.ClusterRoleBinding](t, manifests, name, "clusterrolebinding")
}

type manifestObject interface {
	runtime.Object
	metav1.Object
}

func findManifestByName[T manifestObject](t *testing.T, manifests []runtime.Object, name, kind string) T {
	t.Helper()

	for _, manifest := range manifests {
		obj, ok := manifest.(T)
		if ok && obj.GetName() == name {
			return obj
		}
	}

	t.Fatalf("%s %q not found", kind, name)
	var zero T
	return zero
}

func findHostedManifest[T manifestObject](t *testing.T, manifests []runtime.Object, name, location, kind string) T {
	t.Helper()

	for _, manifest := range manifests {
		obj, ok := manifest.(T)
		if ok && obj.GetName() == name && hostedLocation(obj) == location {
			return obj
		}
	}

	t.Fatalf("%s %q with hosted location %q not found", kind, name, location)
	var zero T
	return zero
}

func assertHostedManifestMissing[T manifestObject](t *testing.T, manifests []runtime.Object, name, location, kind string) {
	t.Helper()

	for _, manifest := range manifests {
		obj, ok := manifest.(T)
		if ok && obj.GetName() == name && hostedLocation(obj) == location {
			t.Fatalf("%s %q with hosted location %q should not be rendered", kind, name, location)
		}
	}
}

func hostedLocation(obj metav1.Object) string {
	return obj.GetAnnotations()[addonv1beta1.HostedManifestLocationAnnotationKey]
}

func assertRule(t *testing.T, rules []rbacv1.PolicyRule, apiGroups, resources, verbs, resourceNames []string) {
	t.Helper()

	assert.Contains(t, rules, rbacv1.PolicyRule{
		APIGroups:     apiGroups,
		Resources:     resources,
		Verbs:         verbs,
		ResourceNames: resourceNames,
	})
}

func findNamespaces(manifests []runtime.Object) []*corev1.Namespace {
	namespaces := []*corev1.Namespace{}
	for _, manifest := range manifests {
		namespace, ok := manifest.(*corev1.Namespace)
		if ok {
			namespaces = append(namespaces, namespace)
		}
	}
	return namespaces
}

func assertDeploymentSecretVolume(t *testing.T, deployment *appsv1.Deployment, volumeName, secretName string) {
	t.Helper()

	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name != volumeName {
			continue
		}
		if assert.NotNil(t, volume.Secret, "volume %q should use a secret", volumeName) {
			assert.Equal(t, secretName, volume.Secret.SecretName)
		}
		return
	}
	t.Fatalf("volume %q not found", volumeName)
}

func assertDeploymentMissingVolume(t *testing.T, deployment *appsv1.Deployment, volumeName string) {
	t.Helper()

	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == volumeName {
			t.Fatalf("volume %q should not be rendered", volumeName)
		}
	}
}

func assertDeploymentVolumeMount(t *testing.T, deployment *appsv1.Deployment, volumeName, mountPath string) {
	t.Helper()

	for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == volumeName {
			assert.Equal(t, mountPath, mount.MountPath)
			assert.True(t, mount.ReadOnly)
			return
		}
	}
	t.Fatalf("volume mount %q not found", volumeName)
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

func newTestHostedAddOn(name, namespace, hostingClusterName string) *addonv1beta1.ManagedClusterAddOn {
	addon := newTestAddOn(name, namespace)
	addon.Annotations = map[string]string{}
	addon.Annotations[addonv1beta1.HostingClusterNameAnnotationKey] = hostingClusterName
	addon.Annotations[addonv1beta1.InstallNamespaceAnnotation] = name
	return addon
}

func newTestAddOn(name, namespace string) *addonv1beta1.ManagedClusterAddOn {
	return &addonv1beta1.ManagedClusterAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func newTestAddOnDeploymentConfig(clusterName, name, installNamespace string) *addonv1beta1.AddOnDeploymentConfig {
	return &addonv1beta1.AddOnDeploymentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: clusterName,
		},
		Spec: addonv1beta1.AddOnDeploymentConfigSpec{
			AgentInstallNamespace: installNamespace,
		},
	}
}

func newTestConfigReferences(config *addonv1beta1.AddOnDeploymentConfig) []addonv1beta1.ConfigReference {
	return []addonv1beta1.ConfigReference{
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
}
