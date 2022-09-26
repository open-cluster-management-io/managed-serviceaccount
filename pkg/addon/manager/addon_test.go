package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

func TestAddOnAgent(t *testing.T) {
	clusterName := "cluster1"
	addonName := "addon1"
	imageName := "imageName1"

	deployManifests := []string{
		addonName,
		"managed-serviceaccount",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"open-cluster-management:managed-serviceaccount:addon-agent",
		"managed-serviceaccount-addon-agent",
	}

	cases := []struct {
		name            string
		installAll      bool
		imagePullSecret *corev1.Secret
		expectManifests []string
	}{
		{
			name:            "not install all",
			expectManifests: deployManifests,
		},
		{
			name:            "install all",
			installAll:      true,
			expectManifests: deployManifests,
		},
		{
			name:            "install all with image pull secret",
			installAll:      true,
			imagePullSecret: &corev1.Secret{},
			expectManifests: append(deployManifests, "open-cluster-management-image-pull-credentials"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeKubeClient := fakekube.NewSimpleClientset()
			addonAgent := NewManagedServiceAccountAddonAgent(fakeKubeClient, imageName, c.installAll, c.imagePullSecret)

			cluster := &clusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterName,
				},
			}
			addon := &addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Name:      addonName,
					Namespace: clusterName,
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: addonName,
				},
			}

			// check manifests
			manifests, err := addonAgent.Manifests(cluster, addon)
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
			assert.ElementsMatch(t, c.expectManifests, actual, "unmatch manifests")

			// check addon options
			addonOptions := addonAgent.GetAgentAddonOptions()
			assert.NotNil(t, addonOptions.Registration, "registration is not specified")
			assert.NotNil(t, addonOptions.Registration.PermissionConfig, "permissionConfig is not specified")

			// check permission config
			err = addonOptions.Registration.PermissionConfig(cluster, addon)
			assert.NoError(t, err)
			actions := fakeKubeClient.Actions()
			assert.Len(t, actions, 2)
			role := actions[0].(clienttesting.CreateAction).GetObject().(*rbacv1.Role)
			assert.Equal(t, clusterName, role.Namespace, "invalid role ns")
			assert.Equal(t, "managed-serviceaccount-addon-agent", role.Name, "invalid role name")
			rolebinding := actions[1].(clienttesting.CreateAction).GetObject().(*rbacv1.RoleBinding)
			assert.Equal(t, clusterName, rolebinding.Namespace, "invalid rolebinding ns")
			assert.Equal(t, "managed-serviceaccount-addon-agent", rolebinding.Name, "invalid rolebinding name")

			if c.installAll {
				assert.Equal(t, agent.InstallAll, addonOptions.InstallStrategy.Type, "invalid install strategy type")
				assert.Equal(t, common.AddonAgentInstallNamespace, addonOptions.InstallStrategy.InstallNamespace, "invalid install ns")
			}
		})
	}
}
