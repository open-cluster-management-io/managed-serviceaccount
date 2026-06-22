package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes/fake"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"

	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

var _ ctrlmanager.LeaderElectionRunnable = addonManagerRunnable{}

func TestValidateDeployMode(t *testing.T) {
	cases := []struct {
		name          string
		deployMode    string
		expectedError string
	}{
		{
			name:       "deployment",
			deployMode: deployModeDeployment,
		},
		{
			name:       "addon template",
			deployMode: deployModeAddOnTemplate,
		},
		{
			name:          "empty",
			expectedError: `unsupported --deploy-mode "", must be "Deployment" or "AddOnTemplate"`,
		},
		{
			name:          "unknown",
			deployMode:    "AddOnTemplte",
			expectedError: `unsupported --deploy-mode "AddOnTemplte", must be "Deployment" or "AddOnTemplate"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := (&HubManagerOptions{DeployMode: c.deployMode}).validateDeployMode()
			if len(c.expectedError) > 0 {
				assert.EqualError(t, err, c.expectedError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetAgentImagePullSecret(t *testing.T) {
	ctx := context.Background()
	namespace := "hub"
	secretName := "pull-secret"
	dockerConfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte("test"),
		},
	}

	t.Run("not configured", func(t *testing.T) {
		secret, err := getAgentImagePullSecret(ctx, fake.NewSimpleClientset(), namespace, "")

		assert.NoError(t, err)
		assert.Nil(t, secret)
	})

	t.Run("configured", func(t *testing.T) {
		secret, err := getAgentImagePullSecret(ctx, fake.NewSimpleClientset(dockerConfigSecret), namespace, secretName)

		assert.NoError(t, err)
		assert.Equal(t, dockerConfigSecret, secret)
	})

	t.Run("missing secret", func(t *testing.T) {
		secret, err := getAgentImagePullSecret(ctx, fake.NewSimpleClientset(), namespace, secretName)

		assert.Nil(t, secret)
		assert.ErrorContains(t, err, "fail to get agent image pull secret")
	})

	t.Run("wrong secret type", func(t *testing.T) {
		secret, err := getAgentImagePullSecret(ctx, fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
			Type: corev1.SecretTypeOpaque,
		}), namespace, secretName)

		assert.Nil(t, secret)
		assert.ErrorContains(t, err, "incorrect type for agent image pull secret")
	})
}

func TestManagedServiceAccountCacheOptions(t *testing.T) {
	t.Run("deployment mode limits the managed cluster addon cache", func(t *testing.T) {
		options := managedServiceAccountCacheOptions(deployModeDeployment)
		var selector fields.Selector
		for object, byObject := range options.ByObject {
			if _, ok := object.(*addonv1beta1.ManagedClusterAddOn); ok {
				selector = byObject.Field
			}
		}

		if assert.NotNil(t, selector) {
			assert.True(t, selector.Matches(fields.Set{"metadata.name": common.AddonName}))
			assert.False(t, selector.Matches(fields.Set{"metadata.name": "another-addon"}))
		}
	})

	t.Run("addon template mode does not require the managed cluster addon API", func(t *testing.T) {
		assert.Empty(t, managedServiceAccountCacheOptions(deployModeAddOnTemplate).ByObject)
	})
}

func TestAddonManagerRunnableRequiresLeaderElection(t *testing.T) {
	started := false
	runnable := addonManagerRunnable{start: func(context.Context) error {
		started = true
		return nil
	}}

	assert.True(t, runnable.NeedLeaderElection())
	assert.NoError(t, runnable.Start(context.Background()))
	assert.True(t, started)
}
