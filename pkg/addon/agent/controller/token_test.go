package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakekube "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/pkg/generated/clientset/versioned/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcile(t *testing.T) {
	clusterName := "cluster1"
	msaName := "msa1"
	token1 := "token1"
	token2 := "token2"
	ca1 := "ca1"
	ca2 := "ca2"

	cases := []struct {
		name                   string
		msa                    *authv1alpha1.ManagedServiceAccount
		sa                     *corev1.ServiceAccount
		secret                 *corev1.Secret
		getError               error
		newToken               string
		isExistingTokenInvalid bool
		expectedError          string
		validateFunc           func(t *testing.T, hubClient client.Client, actions []clienttesting.Action)
	}{
		{
			name: "not found",
			validateFunc: func(t *testing.T, hubClient client.Client, actions []clienttesting.Action) {
				assertActions(t, actions,
					"delete", // delete service account
				)
			},
		},
		{
			name:          "error to get msa",
			getError:      errors.New("internal error"),
			expectedError: "fail to get managed serviceaccount: internal error",
		},
		{
			name:     "create token",
			sa:       newServiceAccount(clusterName, msaName),
			msa:      newManagedServiceAccount(clusterName, msaName).build(),
			newToken: token1,
			validateFunc: func(t *testing.T, hubClient client.Client, actions []clienttesting.Action) {
				assertActions(t, actions, "create", // create serviceaccount
					"create", // create tokenrequest
				)

				assertToken(t, hubClient, clusterName, msaName, token1, ca1)
				assertMSAConditions(t, hubClient, clusterName, msaName, []metav1.Condition{
					{
						Type:   authv1alpha1.ConditionTypeTokenReported,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   authv1alpha1.ConditionTypeSecretCreated,
						Status: metav1.ConditionTrue,
					},
				})
			},
		},
		{
			name:   "token exists",
			sa:     newServiceAccount(clusterName, msaName),
			secret: newSecret(clusterName, msaName, token1, ca1),
			msa: newManagedServiceAccount(clusterName, msaName).
				withRotationValidity(500*time.Second).
				withTokenSecretRef(msaName, time.Now().Add(300*time.Second)).
				build(),
			newToken: token1,
			validateFunc: func(t *testing.T, hubClient client.Client, actions []clienttesting.Action) {
				assertActions(t, actions, "create", // create serviceaccount
					"create", // create tokenreview
				)
				assertToken(t, hubClient, clusterName, msaName, token1, ca1)
			},
		},
		{
			name:   "refresh expiring token",
			sa:     newServiceAccount(clusterName, msaName),
			secret: newSecret(clusterName, msaName, token1, ca1),
			msa: newManagedServiceAccount(clusterName, msaName).
				withRotationValidity(500*time.Second).
				withTokenSecretRef(msaName, time.Now().Add(80*time.Second)).
				build(),
			newToken: token2,
			validateFunc: func(t *testing.T, hubClient client.Client, actions []clienttesting.Action) {
				assertActions(t, actions, "create", // create serviceaccount
					"create", // create tokenreview
				)
				assertToken(t, hubClient, clusterName, msaName, token2, ca1)
				assertMSAConditions(t, hubClient, clusterName, msaName, []metav1.Condition{
					{
						Type:   authv1alpha1.ConditionTypeTokenReported,
						Status: metav1.ConditionTrue,
					},
				})
			},
		},
		{
			name:   "refresh invalid token",
			sa:     newServiceAccount(clusterName, msaName),
			secret: newSecret(clusterName, msaName, token2, ca2),
			msa: newManagedServiceAccount(clusterName, msaName).
				withRotationValidity(500*time.Second).
				withTokenSecretRef(msaName, time.Now().Add(300*time.Second)).
				build(),
			newToken:               token1,
			isExistingTokenInvalid: true,
			validateFunc: func(t *testing.T, hubClient client.Client, actions []clienttesting.Action) {
				assertActions(t, actions, "create", // create serviceaccount
					"create", // create tokenreview
					"create", // create tokenreview
				)
				assertToken(t, hubClient, clusterName, msaName, token1, ca1)
				assertMSAConditions(t, hubClient, clusterName, msaName, []metav1.Condition{
					{
						Type:   authv1alpha1.ConditionTypeTokenReported,
						Status: metav1.ConditionTrue,
					},
				})
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// create fake kube client of the managed cluster
			objs := []runtime.Object{}
			if c.sa != nil {
				objs = append(objs, c.sa)
			}
			fakeKubeClient := fakekube.NewSimpleClientset(objs...)
			fakeKubeClient.PrependReactor(
				"create",
				"serviceaccounts",
				func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
					if action.GetSubresource() == "token" {
						return true, &authv1.TokenRequest{
							Status: authv1.TokenRequestStatus{
								Token:               c.newToken,
								ExpirationTimestamp: metav1.NewTime(time.Now().Add(500 * time.Second)),
							},
						}, nil
					}
					return false, nil, nil
				},
			)

			fakeKubeClient.PrependReactor(
				"create",
				"tokenreviews",
				func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &authv1.TokenReview{
						Status: authv1.TokenReviewStatus{
							Authenticated: !c.isExistingTokenInvalid,
						},
					}, nil
				},
			)

			// create fake client of the hub cluster
			testscheme := scheme.Scheme
			authv1alpha1.AddToScheme(testscheme)
			corev1.AddToScheme(testscheme)
			objs = []runtime.Object{}
			if c.msa != nil {
				objs = append(objs, c.msa)
			}
			if c.secret != nil {
				objs = append(objs, c.secret)
			}
			hubClient := fake.NewFakeClientWithScheme(testscheme, objs...)

			reconciler := TokenReconciler{
				Cache: &fakeCache{
					msa:      c.msa,
					getError: c.getError,
				},
				SpokeNativeClient: fakeKubeClient,
				HubClient:         hubClient,
				SpokeClientConfig: &rest.Config{
					TLSClientConfig: rest.TLSClientConfig{
						CAData: []byte(ca1),
					},
				},
			}

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      msaName,
				Namespace: clusterName,
			}})

			if err == nil {
				if c.validateFunc != nil {
					c.validateFunc(t, hubClient, fakeKubeClient.Actions())
				}
				return
			}
			assert.EqualError(t, err, c.expectedError)
		})
	}
}

func TestMergeConditions(t *testing.T) {
	cases := []struct {
		name     string
		old      []metav1.Condition
		new      []metav1.Condition
		expected []metav1.Condition
	}{
		{
			name: "fully overwrite",
			old: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionTrue,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionTrue,
				},
			},
			new: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionFalse,
				},
			},
			expected: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionFalse,
				},
			},
		},
		{
			name: "overwrite and append",
			old: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionTrue,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionTrue,
				},
			},
			new: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
			},
			expected: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionTrue,
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := mergeConditions(c.old, c.new)
			assert.Equal(t, c.expected, out)
		})
	}

}

type fakeCache struct {
	msa      *authv1alpha1.ManagedServiceAccount
	getError error
}

func (f fakeCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if f.getError != nil {
		return f.getError
	}

	msa, ok := obj.(*authv1alpha1.ManagedServiceAccount)
	if !ok {
		panic("implement me")
	}

	if f.msa == nil {
		return apierrors.NewNotFound(schema.GroupResource{
			Group:    authv1alpha1.GroupVersion.Group,
			Resource: "managedserviceaccounts",
		}, key.Name)
	}

	f.msa.DeepCopyInto(msa)
	return nil
}

func (f fakeCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	panic("implement me")
}

func (f fakeCache) GetInformer(ctx context.Context, obj client.Object) (cache.Informer, error) {
	panic("implement me")
}

func (f fakeCache) Start(ctx context.Context) error {
	panic("implement me")
}

func (f fakeCache) WaitForCacheSync(ctx context.Context) bool {
	panic("implement me")
}

func (f fakeCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	panic("implement me")
}

func (f fakeCache) Set(key string, responseBytes []byte) {
	panic("implement me")
}

func (f fakeCache) Delete(key string) {
	panic("implement me")
}

func (f fakeCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind) (cache.Informer, error) {
	panic("implement me")
}

type managedServiceAccountBuilder struct {
	msa *authv1alpha1.ManagedServiceAccount
}

func newManagedServiceAccount(namespace, name string) *managedServiceAccountBuilder {
	return &managedServiceAccountBuilder{
		msa: &authv1alpha1.ManagedServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		},
	}
}

func (b *managedServiceAccountBuilder) build() *authv1alpha1.ManagedServiceAccount {
	return b.msa
}

func (b *managedServiceAccountBuilder) withRotationValidity(duration time.Duration) *managedServiceAccountBuilder {
	b.msa.Spec.Rotation.Validity = metav1.Duration{
		Duration: duration,
	}
	return b
}

func (b *managedServiceAccountBuilder) withTokenSecretRef(secretName string, expirationTimestamp time.Time) *managedServiceAccountBuilder {
	b.msa.Status.TokenSecretRef = &authv1alpha1.SecretRef{
		Name: secretName,
	}
	timestamp := metav1.NewTime(expirationTimestamp)
	b.msa.Status.ExpirationTimestamp = &timestamp
	return b
}

func newSecret(namespace, name, token, ca string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{},
	}
	if len(token) != 0 {
		secret.Data[corev1.ServiceAccountTokenKey] = []byte(token)
	}
	if len(ca) != 0 {
		secret.Data[corev1.ServiceAccountRootCAKey] = []byte(ca)
	}
	return secret
}

func newServiceAccount(namespace, name string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

// assertActions asserts the actual actions have the expected action verb
func assertActions(t *testing.T, actualActions []clienttesting.Action, expectedVerbs ...string) {
	if len(actualActions) != len(expectedVerbs) {
		t.Fatalf("expected %d call but got: %#v", len(expectedVerbs), actualActions)
	}
	for i, expected := range expectedVerbs {
		if actualActions[i].GetVerb() != expected {
			t.Errorf("expected %s action but got: %#v", expected, actualActions[i])
		}
	}
}

func assertToken(t *testing.T, client client.Client, secretNamespace, secretName, token, ca string) {
	secret := &corev1.Secret{}
	err := client.Get(context.TODO(), types.NamespacedName{
		Namespace: secretNamespace,
		Name:      secretName,
	}, secret)

	assert.NoError(t, err)
	tokenData, ok := secret.Data[corev1.ServiceAccountTokenKey]
	assert.True(t, ok, "token not exists in secret %s/%s", secretNamespace, secretName)
	assert.Equal(t, token, string(tokenData), "unexpected token")

	caData, ok := secret.Data[corev1.ServiceAccountRootCAKey]
	assert.True(t, ok, "ca not exists in secret %s/%s", secretNamespace, secretName)
	assert.Equal(t, ca, string(caData), "unexpected ca")
}

func assertMSAConditions(t *testing.T, client client.Client, msaNamespace, msaName string, expected []metav1.Condition) {
	msa := &authv1alpha1.ManagedServiceAccount{}
	err := client.Get(context.TODO(), types.NamespacedName{
		Namespace: msaNamespace,
		Name:      msaName,
	}, msa)

	assert.NoError(t, err)
	for _, condition := range expected {
		assert.True(t, meta.IsStatusConditionPresentAndEqual(msa.Status.Conditions, condition.Type, condition.Status),
			"condition %q with status %v not found", condition.Type, condition.Status)
	}
}
