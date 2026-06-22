package manager

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	"open-cluster-management.io/addon-framework/pkg/utils"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	fakeaddon "open-cluster-management.io/api/client/addon/clientset/versioned/fake"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/provisioner"
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
			getValuesFunc:         []addonfactory.GetValuesFunc{GetDefaultValues(imageName, nil, false)},
			expectedManifestNames: manifestNames,
			expectedImage:         imageName,
		},
		{
			name:                  "install all with image pull secret",
			getValuesFunc:         []addonfactory.GetValuesFunc{GetDefaultValues(imageName, newTestImagePullSecret(), false)},
			expectedManifestNames: append(manifestNames, "open-cluster-management-image-pull-credentials"),
			expectedImage:         imageName,
		},
		{
			name: "node placement is rendered on the agent deployment",
			getValuesFunc: []addonfactory.GetValuesFunc{
				GetDefaultValues(imageName, nil, false),
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

			values, err := GetDefaultValues("imageName1", imagePullSecret, false)(newTestCluster("cluster1"), newTestAddOn(common.AddonName, "cluster1"))

			assert.Nil(t, values)
			assert.ErrorContains(t, err, `missing ".dockerconfigjson"`)
		})
	}
}

func TestAgentChartRotationDefaultsMatchProvisioner(t *testing.T) {
	data, err := FS.ReadFile("manifests/charts/managed-serviceaccount-agent/values.yaml")
	assert.NoError(t, err)

	values := struct {
		TokenExpirationSeconds int64  `json:"managedKubeConfigTokenExpirationSeconds"`
		RefreshBefore          string `json:"managedKubeConfigRefreshBefore"`
		SyncInterval           string `json:"managedKubeConfigProvisionerSyncInterval"`
	}{}
	assert.NoError(t, yaml.Unmarshal(data, &values))
	assert.Equal(t, provisioner.DefaultTokenExpirationSeconds, values.TokenExpirationSeconds)
	refreshBefore, err := time.ParseDuration(values.RefreshBefore)
	assert.NoError(t, err)
	assert.Equal(t, provisioner.DefaultRefreshBefore, refreshBefore)
	syncInterval, err := time.ParseDuration(values.SyncInterval)
	assert.NoError(t, err)
	assert.Equal(t, provisioner.DefaultSyncInterval, syncInterval)
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
		GetDefaultValues("imageName1", nil, false),
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
			GetDefaultValues("imageName1", nil, false),
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

func TestManifestAddonAgentDeploymentOnManagedCluster(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	// An addon without a hosting-cluster annotation still renders the agent on the
	// managed cluster.
	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestAddOn(addonName, clusterName),
		GetDefaultValues(imageName, nil, false),
	)
	deployment := findDeployment(t, manifests)
	container := deployment.Spec.Template.Spec.Containers[0]

	assert.NotContains(t, deployment.Annotations, addonv1beta1.HostedManifestLocationAnnotationKey)
	assert.Contains(t, container.Args, "--leader-elect=false")
	assert.Contains(t, container.Args, "--cluster-name="+clusterName)
	assert.Contains(t, container.Args, "--install-mode=Default")
	assert.Contains(t, container.Args, "--kubeconfig=/etc/hub/kubeconfig")
	assert.Contains(t, container.Args, "--lease-health-check=true")
	// The args and volumes hosted gates are independent template blocks, so
	// the missing-volume assert below does not cover this flag.
	assert.NotContains(t, container.Args, "--spoke-kubeconfig=/etc/managed/kubeconfig")
	assertDeploymentSecretVolume(t, deployment, "hub-kubeconfig", "managed-serviceaccount-hub-kubeconfig")
	assertDeploymentMissingVolume(t, deployment, "managed-kubeconfig")

	role := findRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assertRule(t, role.Rules, []string{"coordination.k8s.io"}, []string{"leases"}, []string{"get", "create", "update", "patch"}, nil)
	assertHostedManifestMissing[*rbacv1.Role](t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
}

func TestManifestAddonAgentHostedModeDeployment(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
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

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"managedKubeConfigSecret": managedKubeConfigSecret,
			}, nil
		},
	)
	deployment := findDeployment(t, manifests)

	assertDeploymentSecretVolume(t, deployment, "managed-kubeconfig", managedKubeConfigSecret)
}

func TestManifestAddonAgentHostedModeSecretNameCannotInjectManifestStructure(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	// Bypasses ValidateAddOnAgentVariables on purpose: the chart's own quoting
	// must keep a newline-bearing value a single scalar even if validation
	// regresses. The indentation lands hostNetwork in the pod spec if quoting
	// is ever removed, which the HostNetwork assert below would catch.
	injectedValue := "evil\n      hostNetwork: true"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"managedKubeConfigSecret": injectedValue,
			}, nil
		},
	)
	deployment := findDeployment(t, manifests)

	assert.False(t, deployment.Spec.Template.Spec.HostNetwork)
	assertDeploymentSecretVolume(t, deployment, "managed-kubeconfig", injectedValue)
}

func TestManifestAddonAgentHostedModeManifestLocations(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, newTestImagePullSecret(), false),
	)

	// The finders match on name and hosted location, so a successful lookup is the assertion.
	findServiceAccount(t, manifests, "managed-serviceaccount", "")
	findServiceAccount(t, manifests, "managed-serviceaccount", addonv1beta1.HostedManifestLocationHostingValue)
	findRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	findRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assertHostedManifestMissing[*rbacv1.Role](
		t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent",
		addonv1beta1.HostedManifestLocationHostingValue)
	assertHostedManifestMissing[*rbacv1.RoleBinding](
		t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent",
		addonv1beta1.HostedManifestLocationHostingValue)

	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue, hostedLocation(findDeployment(t, manifests)))
	assert.Equal(t, addonv1beta1.HostedManifestLocationHostingValue,
		hostedLocation(findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")))
	findSecret(t, manifests, "open-cluster-management-image-pull-credentials", addonv1beta1.HostedManifestLocationHostingValue)
	findServiceAccount(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)
	findRole(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)
	findRoleBinding(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)
	findRoleBinding(t, manifests, testProvisionerSourceRBACName(addonName), addonv1beta1.HostedManifestLocationHostingValue)
	findRole(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
	findRoleBinding(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
	assertHostedManifestMissing[*networkingv1.NetworkPolicy](
		t, manifests, "managed-serviceaccount-kubeconfig-provisioner-network-policy",
		addonv1beta1.HostedManifestLocationHostingValue)

	// An unannotated manifest goes to the managed cluster, like the Role/RoleBinding/ServiceAccount above.
	assert.Empty(t,
		hostedLocation(findClusterRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent")))
	assert.Empty(t,
		hostedLocation(findClusterRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent")))
}

func TestManifestAddonAgentHostedModeLeaseRBAC(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
	)

	deployment := findDeployment(t, manifests)
	assert.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, "--lease-health-check=true")
	assert.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, "--install-mode=Hosted")

	role := findRole(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assert.Equal(t, addonName, role.Namespace)
	for _, rule := range role.Rules {
		assert.NotContains(t, rule.APIGroups, "coordination.k8s.io",
			"an agent running on a hosting cluster should have lease permissions only there")
	}

	binding := findRoleBinding(t, manifests, "open-cluster-management:managed-serviceaccount:addon-agent", "")
	assert.Equal(t, addonName, binding.Namespace)
	assertRoleBindingBinds(t, binding, "open-cluster-management:managed-serviceaccount:addon-agent", "managed-serviceaccount", addonName)

	hostingRole := findRole(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, hostingRole.Namespace)
	assertRule(t, hostingRole.Rules, []string{"coordination.k8s.io"}, []string{"leases"}, []string{"create"}, nil)
	assertRule(t, hostingRole.Rules, []string{"coordination.k8s.io"}, []string{"leases"}, []string{"get", "update", "patch"}, []string{"managed-serviceaccount"})

	hostingBinding := findRoleBinding(t, manifests, "managed-serviceaccount-health-lease", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, hostingBinding.Namespace)
	assertRoleBindingBinds(t, hostingBinding, "managed-serviceaccount-health-lease", "managed-serviceaccount", addonName)
}

func TestManifestAddonAgentHostedModeExternalManagedKubeConfigOverrides(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"externalManagedKubeConfigNamespace": "custom-source-ns",
				"externalManagedKubeConfigSecret":    "custom-source-secret",
				"managedKubeConfigSecret":            "custom-target-secret",
			}, nil
		},
	)

	prov := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	args := prov.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--source-namespace=custom-source-ns")
	assert.Contains(t, args, "--source-secret=custom-source-secret")
	assert.Contains(t, args, "--target-secret=custom-target-secret")

	targetRole := findRole(t, manifests, "managed-serviceaccount-kubeconfig-provisioner", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, targetRole.Namespace)
	assertRule(t, targetRole.Rules, []string{""}, []string{"secrets"}, []string{"get", "update"}, []string{"custom-target-secret"})
	assertRule(t, targetRole.Rules, []string{""}, []string{"secrets"}, []string{"create"}, nil)
	assertRule(t, targetRole.Rules, []string{""}, []string{"serviceaccounts"}, []string{"get"}, []string{"managed-serviceaccount-kubeconfig-provisioner"})
	assertRule(t, targetRole.Rules, []string{"events.k8s.io"}, []string{"events"}, []string{"create", "patch", "update"}, nil)

	sourceRBACName := testProvisionerSourceRBACName(addonName)
	sourceRole := findRole(t, manifests, sourceRBACName, addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, "custom-source-ns", sourceRole.Namespace)
	assertRule(t, sourceRole.Rules, []string{""}, []string{"secrets"}, []string{"get"}, []string{"custom-source-secret"})

	sourceBinding := findRoleBinding(t, manifests, sourceRBACName, addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, "custom-source-ns", sourceBinding.Namespace)
	assertRoleBindingBinds(t, sourceBinding, sourceRBACName, "managed-serviceaccount-kubeconfig-provisioner", addonName)
}

func TestManifestAddonAgentHostedModeSourceRBACNamesAreUniqueInSharedNamespace(t *testing.T) {
	sourceNamespace := "shared-source"
	firstAddonName := "addon1"
	secondAddonName := "addon2"

	render := func(clusterName, addonName, sourceSecret string) []runtime.Object {
		return renderTestManifests(
			t,
			newTestCluster(clusterName),
			newTestHostedAddOn(addonName, clusterName, "hosting1"),
			GetDefaultValues("imageName1", nil, false),
			func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
				return addonfactory.Values{
					"externalManagedKubeConfigNamespace": sourceNamespace,
					"externalManagedKubeConfigSecret":    sourceSecret,
				}, nil
			},
		)
	}

	firstManifests := render("cluster1", firstAddonName, "cluster1-kubeconfig")
	secondManifests := render("cluster2", secondAddonName, "cluster2-kubeconfig")
	firstName := testProvisionerSourceRBACName(firstAddonName)
	secondName := testProvisionerSourceRBACName(secondAddonName)

	assert.NotEqual(t, firstName, secondName)
	for _, test := range []struct {
		manifests        []runtime.Object
		name             string
		sourceSecret     string
		subjectNamespace string
	}{
		{
			manifests:        firstManifests,
			name:             firstName,
			sourceSecret:     "cluster1-kubeconfig",
			subjectNamespace: firstAddonName,
		},
		{
			manifests:        secondManifests,
			name:             secondName,
			sourceSecret:     "cluster2-kubeconfig",
			subjectNamespace: secondAddonName,
		},
	} {
		role := findRole(t, test.manifests, test.name, addonv1beta1.HostedManifestLocationHostingValue)
		assert.Equal(t, sourceNamespace, role.Namespace)
		assertRule(t, role.Rules, []string{""}, []string{"secrets"}, []string{"get"}, []string{test.sourceSecret})

		binding := findRoleBinding(t, test.manifests, test.name, addonv1beta1.HostedManifestLocationHostingValue)
		assert.Equal(t, sourceNamespace, binding.Namespace)
		assertRoleBindingBinds(t, binding, test.name, "managed-serviceaccount-kubeconfig-provisioner", test.subjectNamespace)
	}
}

func TestManifestAddonAgentHostedModeProvisioner(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
	nodeSelector := map[string]string{"node-role.kubernetes.io/hosting": "true"}
	tolerations := []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "hosting", Effect: corev1.TaintEffectNoSchedule}}

	cases := []struct {
		name     string
		values   addonfactory.GetValuesFunc
		validate func(t *testing.T, manifests []runtime.Object, prov *appsv1.Deployment)
	}{
		{
			name: "source configuration and RBAC",
			validate: func(t *testing.T, manifests []runtime.Object, prov *appsv1.Deployment) {
				args := prov.Spec.Template.Spec.Containers[0].Args
				assert.Contains(t, args, "--source-namespace="+clusterName)
				assert.Contains(t, args, "--source-secret=external-managed-kubeconfig")
				assert.Contains(t, args, "--target-secret="+addonName+"-managed-kubeconfig")
				assert.Contains(t, args, "--hosting-service-account-name=managed-serviceaccount-kubeconfig-provisioner")

				sourceRBACName := testProvisionerSourceRBACName(addonName)
				sourceRole := findRole(t, manifests, sourceRBACName, addonv1beta1.HostedManifestLocationHostingValue)
				assert.Equal(t, clusterName, sourceRole.Namespace)
				assertRule(t, sourceRole.Rules, []string{""}, []string{"secrets"}, []string{"get"}, []string{"external-managed-kubeconfig"})

				sourceBinding := findRoleBinding(t, manifests, sourceRBACName, addonv1beta1.HostedManifestLocationHostingValue)
				assert.Equal(t, clusterName, sourceBinding.Namespace)
				assertRoleBindingBinds(t, sourceBinding, sourceRBACName, "managed-serviceaccount-kubeconfig-provisioner", addonName)
			},
		},
		{
			name: "timing defaults",
			validate: func(t *testing.T, _ []runtime.Object, prov *appsv1.Deployment) {
				args := prov.Spec.Template.Spec.Containers[0].Args
				assert.Contains(t, args, fmt.Sprintf("--token-expiration-seconds=%d", provisioner.DefaultTokenExpirationSeconds))
				assert.Contains(t, args, "--refresh-before="+provisioner.DefaultRefreshBefore.String())
				assert.Contains(t, args, "--sync-interval="+provisioner.DefaultSyncInterval.String())
			},
		},
		{
			name: "timing overrides",
			values: func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
				return addonfactory.Values{
					"managedKubeConfigTokenExpirationSeconds":  int64(7200),
					"managedKubeConfigRefreshBefore":           "15m",
					"managedKubeConfigProvisionerSyncInterval": "30s",
				}, nil
			},
			validate: func(t *testing.T, _ []runtime.Object, prov *appsv1.Deployment) {
				args := prov.Spec.Template.Spec.Containers[0].Args
				assert.Contains(t, args, "--token-expiration-seconds=7200")
				assert.Contains(t, args, "--refresh-before=15m")
				assert.Contains(t, args, "--sync-interval=30s")
			},
		},
		{
			name: "resource requirements",
			values: func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
				return addonfactory.ToAddOnDeploymentConfigValues(addonv1beta1.AddOnDeploymentConfig{
					Spec: addonv1beta1.AddOnDeploymentConfigSpec{
						ResourceRequirements: []addonv1beta1.ContainerResourceRequirements{
							{
								ContainerID: "deployments:managed-serviceaccount-kubeconfig-provisioner:kubeconfig-provisioner",
								Resources:   resources,
							},
						},
					},
				})
			},
			validate: func(t *testing.T, _ []runtime.Object, prov *appsv1.Deployment) {
				if assert.Len(t, prov.Spec.Template.Spec.Containers, 1, "expected one provisioner container") {
					assert.Equal(t, resources, prov.Spec.Template.Spec.Containers[0].Resources)
				}
			},
		},
		{
			name: "security context",
			validate: func(t *testing.T, _ []runtime.Object, prov *appsv1.Deployment) {
				assertPodSecurityContext(t, prov.Spec.Template.Spec)
				if assert.Len(t, prov.Spec.Template.Spec.Containers, 1, "expected one provisioner container") {
					assertContainerSecurityContext(t, prov.Spec.Template.Spec.Containers[0])
				}
			},
		},
		{
			name:   "node placement",
			values: getNodePlacementValues(nodeSelector, tolerations),
			validate: func(t *testing.T, _ []runtime.Object, prov *appsv1.Deployment) {
				assert.Equal(t, nodeSelector, prov.Spec.Template.Spec.NodeSelector)
				assert.Equal(t, tolerations, prov.Spec.Template.Spec.Tolerations)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getValuesFuncs := []addonfactory.GetValuesFunc{GetDefaultValues(imageName, nil, false)}
			if c.values != nil {
				getValuesFuncs = append(getValuesFuncs, c.values)
			}
			manifests := renderTestManifests(
				t,
				newTestCluster(clusterName),
				newTestHostedAddOn(addonName, clusterName, "hosting1"),
				getValuesFuncs...,
			)
			c.validate(t, manifests, findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner"))
		})
	}
}

func TestManifestAddonAgentServiceAccountNameOverrideOnManagedCluster(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"
	customName := "custom-msa"
	installNamespace := addonfactory.AddonDefaultInstallNamespace

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestAddOn(addonName, clusterName),
		GetDefaultValues(imageName, nil, false),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"managedServiceAccountName": customName,
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

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
		func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
			return addonfactory.Values{
				"managedServiceAccountName": customName,
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

	prov := findDeploymentByName(t, manifests, "managed-serviceaccount-kubeconfig-provisioner")
	assert.Contains(t, prov.Spec.Template.Spec.Containers[0].Args, "--managed-serviceaccount-name="+customName)

	deployment := findDeployment(t, manifests)
	assert.Equal(t, "managed-serviceaccount", deployment.Spec.Template.Spec.ServiceAccountName)
	hostingSA := findServiceAccount(t, manifests, "managed-serviceaccount", addonv1beta1.HostedManifestLocationHostingValue)
	assert.Equal(t, addonName, hostingSA.Namespace)
}

func TestManifestAddonAgentHostedModeNamespaces(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues(imageName, nil, false),
	)

	assert.Len(t, findNamespaces(manifests), 2)
	findHostedManifest[*corev1.Namespace](t, manifests, addonName, addonv1beta1.HostedManifestLocationHostingValue)
	findHostedManifest[*corev1.Namespace](t, manifests, addonName, "")
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
			manifests := renderWithConfig(t, clusterName, "addon1", "imageName1", false, c.variables...)

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

func TestManifestAddonNetworkPolicy(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	cases := []struct {
		name                  string
		enableNetworkPolicies bool
		prometheusEnabled     bool
		expectNetworkPolicy   bool
		expectMetricsIngress  bool
	}{
		{
			name:                  "disabled by default",
			enableNetworkPolicies: false,
			expectNetworkPolicy:   false,
		},
		{
			name:                  "enabled without prometheus has no metrics ingress",
			enableNetworkPolicies: true,
			expectNetworkPolicy:   true,
			expectMetricsIngress:  false,
		},
		{
			name:                  "enabled with prometheus opens metrics ingress",
			enableNetworkPolicies: true,
			prometheusEnabled:     true,
			expectNetworkPolicy:   true,
			expectMetricsIngress:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var variables []addonv1beta1.CustomizedVariable
			if c.prometheusEnabled {
				variables = []addonv1beta1.CustomizedVariable{
					{Name: prometheusEnabledVariableName, Value: "true"},
				}
			}
			manifests := renderWithConfig(t, clusterName, addonName, imageName, c.enableNetworkPolicies, variables...)

			np := getAgentNetworkPolicy(manifests)
			if !c.expectNetworkPolicy {
				assert.Nil(t, np, "NetworkPolicy should not be rendered when disabled")
				return
			}
			if !assert.NotNil(t, np, "expected agent NetworkPolicy") {
				return
			}
			assert.Equal(t, "managed-serviceaccount-addon-agent-network-policy", np.Name)
			assert.Empty(t, hostedLocation(np), "NetworkPolicy must stay with the agent on the managed cluster")
			assert.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
			assert.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
			assert.True(t, networkPolicyAllowsEgressTCPPort(np, 443))
			assert.True(t, networkPolicyAllowsEgressTCPPort(np, 6443))
			hasMetricsIngress := networkPolicyAllowsIngressTCPPort(np, 38080)
			assert.Equal(t, c.expectMetricsIngress, hasMetricsIngress)
			assert.True(t, networkPolicyAllowsIngressTCPPort(np, 8000),
				"health :8000 must be opened for kubelet livenessProbe")
		})
	}
}

func TestManifestAddonAgentHostedModeNetworkPolicy(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"

	manifests := renderTestManifests(
		t,
		newTestCluster(clusterName),
		newTestHostedAddOn(addonName, clusterName, "hosting1"),
		GetDefaultValues("imageName1", nil, true),
	)

	networkPolicy := findHostedManifest[*networkingv1.NetworkPolicy](
		t,
		manifests,
		"managed-serviceaccount-addon-agent-network-policy",
		addonv1beta1.HostedManifestLocationHostingValue,
	)
	assert.Equal(t, addonName, networkPolicy.Namespace)

	provisionerNetworkPolicy := findHostedManifest[*networkingv1.NetworkPolicy](
		t,
		manifests,
		"managed-serviceaccount-kubeconfig-provisioner-network-policy",
		addonv1beta1.HostedManifestLocationHostingValue,
	)
	assert.Equal(t, addonName, provisionerNetworkPolicy.Namespace)
	assert.Equal(t, map[string]string{
		"addon-agent": "managed-serviceaccount-kubeconfig-provisioner",
	}, provisionerNetworkPolicy.Spec.PodSelector.MatchLabels)
	assert.Contains(t, provisionerNetworkPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
	assert.Contains(t, provisionerNetworkPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
	assert.Empty(t, provisionerNetworkPolicy.Spec.Ingress)
	assert.True(t, networkPolicyAllowsEgressTCPPort(provisionerNetworkPolicy, 443))
	assert.True(t, networkPolicyAllowsEgressTCPPort(provisionerNetworkPolicy, 6443))
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

func TestValidateAddOnAgentVariables(t *testing.T) {
	cases := []struct {
		name          string
		variables     []addonv1beta1.CustomizedVariable
		expectedError bool
	}{
		{
			name: "default values are valid",
		},
		{
			name: "valid values pass through",
			variables: []addonv1beta1.CustomizedVariable{
				{Name: managedKubeConfigTokenExpirationSecondsVariableName, Value: "600"},
				{Name: managedKubeConfigRefreshBeforeVariableName, Value: "599s"},
				{Name: managedKubeConfigProvisionerSyncIntervalVariableName, Value: "30s"},
			},
		},
		{
			name:      "unrelated variables are ignored",
			variables: []addonv1beta1.CustomizedVariable{{Name: "somethingElse", Value: "anything\ngoes"}},
		},
		{
			name:      "allows empty source namespace",
			variables: []addonv1beta1.CustomizedVariable{{Name: externalManagedKubeConfigNamespaceVariableName, Value: ""}},
		},
		{
			name:          "rejects newline in source namespace",
			variables:     []addonv1beta1.CustomizedVariable{{Name: externalManagedKubeConfigNamespaceVariableName, Value: "ns\nextra"}},
			expectedError: true,
		},
		{
			name:          "rejects invalid managed serviceaccount name",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedServiceAccountNameVariableName, Value: "Not_A_Name"}},
			expectedError: true,
		},
		{
			name:          "rejects any hub kubeconfig secret override",
			variables:     []addonv1beta1.CustomizedVariable{{Name: hubKubeConfigSecretVariableName, Value: "valid-secret-name"}},
			expectedError: true,
		},
		{
			name:          "rejects invalid managed kubeconfig secret name",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedKubeConfigSecretVariableName, Value: "Not_A_Secret"}},
			expectedError: true,
		},
		{
			name:          "rejects empty source secret name",
			variables:     []addonv1beta1.CustomizedVariable{{Name: externalManagedKubeConfigSecretVariableName, Value: ""}},
			expectedError: true,
		},
		{
			name:          "rejects non-duration refresh before",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedKubeConfigRefreshBeforeVariableName, Value: "10 minutes"}},
			expectedError: true,
		},
		{
			name:          "rejects non-duration sync interval",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedKubeConfigProvisionerSyncIntervalVariableName, Value: "five minutes"}},
			expectedError: true,
		},
		{
			name:          "rejects non-integer token expiration seconds",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedKubeConfigTokenExpirationSecondsVariableName, Value: "3600s"}},
			expectedError: true,
		},
		{
			name:          "rejects token expiration below the Kubernetes minimum",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedKubeConfigTokenExpirationSecondsVariableName, Value: "599"}},
			expectedError: true,
		},
		{
			name:          "validates customized token lifetime against the default refresh before",
			variables:     []addonv1beta1.CustomizedVariable{{Name: managedKubeConfigTokenExpirationSecondsVariableName, Value: "600"}},
			expectedError: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ValidateAddOnAgentVariables(addonv1beta1.AddOnDeploymentConfig{
				Spec: addonv1beta1.AddOnDeploymentConfigSpec{
					CustomizedVariables: c.variables,
				},
			})
			if c.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func renderTestManifests(
	t *testing.T,
	cluster *clusterv1.ManagedCluster,
	addon *addonv1beta1.ManagedClusterAddOn,
	getValuesFuncs ...addonfactory.GetValuesFunc,
) []runtime.Object {
	return renderTestManifestsWithNamespaceFunc(t, cluster, addon, nil, getValuesFuncs...)
}

func renderTestManifestsWithNamespaceFunc(
	t *testing.T,
	cluster *clusterv1.ManagedCluster,
	addon *addonv1beta1.ManagedClusterAddOn,
	agentInstallNamespace agent.AgentInstallNamespaceFunc,
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
	addOnAgent, err := agentFactory.BuildHelmAgentAddon()
	assert.NoError(t, err)

	manifests, err := addOnAgent.Manifests(context.Background(), cluster, addon)
	assert.NoError(t, err)

	return manifests
}

func renderWithConfig(t *testing.T, clusterName, addonName, imageName string, enableNetworkPolicies bool, variables ...addonv1beta1.CustomizedVariable) []runtime.Object {
	t.Helper()
	config := newTestAddOnDeploymentConfig(clusterName, "metrics-config", addonfactory.AddonDefaultInstallNamespace)
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
		GetDefaultValues(imageName, nil, enableNetworkPolicies),
		addonfactory.GetAddOnDeploymentConfigValues(
			deploymentConfigGetter,
			addonfactory.ToAddOnDeploymentConfigValues,
			ToAddOnPrometheusValues,
		),
	)
}

func getAgentNetworkPolicy(manifests []runtime.Object) *networkingv1.NetworkPolicy {
	for _, manifest := range manifests {
		if np, ok := manifest.(*networkingv1.NetworkPolicy); ok {
			if np.Name == "managed-serviceaccount-addon-agent-network-policy" {
				return np
			}
		}
	}
	return nil
}

func networkPolicyAllowsEgressTCPPort(np *networkingv1.NetworkPolicy, port int32) bool {
	if np == nil {
		return false
	}
	for _, rule := range np.Spec.Egress {
		if len(rule.Ports) == 0 {
			continue
		}
		for _, p := range rule.Ports {
			if p.Protocol != nil && *p.Protocol != corev1.ProtocolTCP {
				continue
			}
			if p.Port != nil && p.Port.IntVal == port {
				return true
			}
		}
	}
	return false
}

func networkPolicyAllowsIngressTCPPort(np *networkingv1.NetworkPolicy, port int32) bool {
	if np == nil {
		return false
	}
	for _, rule := range np.Spec.Ingress {
		if len(rule.Ports) == 0 {
			continue
		}
		for _, p := range rule.Ports {
			if p.Protocol != nil && *p.Protocol != corev1.ProtocolTCP {
				continue
			}
			if p.Port != nil && p.Port.IntVal == port {
				return true
			}
		}
	}
	return false
}

func findDeployment(t *testing.T, manifests []runtime.Object) *appsv1.Deployment {
	t.Helper()
	return findDeploymentByName(t, manifests, "managed-serviceaccount-addon-agent")
}

func findDeploymentByName(t *testing.T, manifests []runtime.Object, name string) *appsv1.Deployment {
	t.Helper()
	return findManifestByName[*appsv1.Deployment](t, manifests, name)
}

func findSecret(t *testing.T, manifests []runtime.Object, name, location string) *corev1.Secret {
	t.Helper()
	return findHostedManifest[*corev1.Secret](t, manifests, name, location)
}

func findServiceAccount(t *testing.T, manifests []runtime.Object, name, location string) *corev1.ServiceAccount {
	t.Helper()
	return findHostedManifest[*corev1.ServiceAccount](t, manifests, name, location)
}

func findRole(t *testing.T, manifests []runtime.Object, name, location string) *rbacv1.Role {
	t.Helper()
	return findHostedManifest[*rbacv1.Role](t, manifests, name, location)
}

func findRoleBinding(t *testing.T, manifests []runtime.Object, name, location string) *rbacv1.RoleBinding {
	t.Helper()
	return findHostedManifest[*rbacv1.RoleBinding](t, manifests, name, location)
}

func findClusterRole(t *testing.T, manifests []runtime.Object, name string) *rbacv1.ClusterRole {
	t.Helper()
	return findManifestByName[*rbacv1.ClusterRole](t, manifests, name)
}

func findClusterRoleBinding(t *testing.T, manifests []runtime.Object, name string) *rbacv1.ClusterRoleBinding {
	t.Helper()
	return findManifestByName[*rbacv1.ClusterRoleBinding](t, manifests, name)
}

type manifestObject interface {
	runtime.Object
	metav1.Object
}

func findManifestByName[T manifestObject](t *testing.T, manifests []runtime.Object, name string) T {
	t.Helper()

	for _, manifest := range manifests {
		obj, ok := manifest.(T)
		if ok && obj.GetName() == name {
			return obj
		}
	}

	var zero T
	t.Fatalf("%T %q not found", zero, name)
	return zero
}

func findHostedManifest[T manifestObject](t *testing.T, manifests []runtime.Object, name, location string) T {
	t.Helper()

	for _, manifest := range manifests {
		obj, ok := manifest.(T)
		if ok && obj.GetName() == name && hostedLocation(obj) == location {
			return obj
		}
	}

	var zero T
	t.Fatalf("%T %q with hosted location %q not found", zero, name, location)
	return zero
}

func assertHostedManifestMissing[T manifestObject](t *testing.T, manifests []runtime.Object, name, location string) {
	t.Helper()

	for _, manifest := range manifests {
		obj, ok := manifest.(T)
		if ok && obj.GetName() == name && hostedLocation(obj) == location {
			t.Fatalf("%T %q with hosted location %q should not be rendered", obj, name, location)
		}
	}
}

func hostedLocation(obj metav1.Object) string {
	return obj.GetAnnotations()[addonv1beta1.HostedManifestLocationAnnotationKey]
}

func testProvisionerSourceRBACName(installNamespace string) string {
	return fmt.Sprintf("managed-serviceaccount-kubeconfig-provisioner-source-%s", installNamespace)
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

func assertRoleBindingBinds(t *testing.T, binding *rbacv1.RoleBinding, roleName, subjectName, subjectNamespace string) {
	t.Helper()

	assert.Equal(t, "Role", binding.RoleRef.Kind)
	assert.Equal(t, roleName, binding.RoleRef.Name)
	assert.Len(t, binding.Subjects, 1)
	assert.Equal(t, "ServiceAccount", binding.Subjects[0].Kind)
	assert.Equal(t, subjectName, binding.Subjects[0].Name)
	assert.Equal(t, subjectNamespace, binding.Subjects[0].Namespace)
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

	if !assert.NotEmpty(t, deployment.Spec.Template.Spec.Containers, "expected at least one container") {
		return
	}
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
	addon.Annotations = map[string]string{
		addonv1beta1.HostingClusterNameAnnotationKey: hostingClusterName,
		addonv1beta1.InstallNamespaceAnnotation:      name,
	}
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
