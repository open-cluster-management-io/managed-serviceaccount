package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
