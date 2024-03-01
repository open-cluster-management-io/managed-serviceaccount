package commoncontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clock "k8s.io/utils/clock/testing"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcile(t *testing.T) {
	clusterName := "cluster1"
	msaName := "msa1"
	now := time.Now()

	cases := []struct {
		name           string
		msa            *authv1beta1.ManagedServiceAccount
		getError       error
		expectedResult reconcile.Result
		expectedError  string
		validateFunc   func(t *testing.T, hubClient client.Client)
	}{
		{
			name: "not found",
		},
		{
			name:          "error to get msa",
			getError:      errors.New("internal error"),
			expectedError: "fail to get managed serviceaccount: internal error",
		},
		{
			name:          "without TTLSecondsAfterCreation specified",
			msa:           newManagedServiceAccount(clusterName, msaName).build(),
			expectedError: "fail to get managed serviceaccount: internal error",
		},
		{
			name: "expired",
			msa:  newManagedServiceAccount(clusterName, msaName).withTTLSecondsAfterCreation(now.Add(-1000*time.Second), 800).build(),
			validateFunc: func(t *testing.T, hubClient client.Client) {
				msa := &authv1beta1.ManagedServiceAccount{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: clusterName,
					Name:      msaName,
				}, msa)

				assert.True(t, apierrors.IsNotFound(err), "msa should have already been deleted")
			},
		},
		{
			name: "not expired yet",
			msa:  newManagedServiceAccount(clusterName, msaName).withTTLSecondsAfterCreation(now, 1000).build(),
			expectedResult: reconcile.Result{
				Requeue:      true,
				RequeueAfter: 1000 * time.Second,
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				msa := &authv1beta1.ManagedServiceAccount{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: clusterName,
					Name:      msaName,
				}, msa)

				assert.NoError(t, err, "unexpected error")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// create fake client of the hub cluster
			testscheme := runtime.NewScheme()
			authv1beta1.AddToScheme(testscheme)

			objs := []runtime.Object{}
			if c.msa != nil {
				objs = append(objs, c.msa)
			}
			hubClient := fake.NewClientBuilder().
				WithScheme(testscheme).
				WithRuntimeObjects(objs...).
				Build()

			reconciler := NewEphemeralIdentityReconciler(
				&fakeCache{
					msa:      c.msa,
					getError: c.getError,
				}, hubClient)
			reconciler.clock = clock.NewFakeClock(now)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      msaName,
				Namespace: clusterName,
			}})
			if err == nil {
				assert.Equal(t, c.expectedResult, result, "invalid result")
				return
			}
			assert.EqualError(t, err, c.expectedError)
		})
	}
}

type fakeCache struct {
	msa      *authv1beta1.ManagedServiceAccount
	getError error
}

func (f fakeCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if f.getError != nil {
		return f.getError
	}

	msa, ok := obj.(*authv1beta1.ManagedServiceAccount)
	if !ok {
		panic("implement me")
	}

	if f.msa == nil {
		return apierrors.NewNotFound(schema.GroupResource{
			Group:    authv1beta1.GroupVersion.Group,
			Resource: "managedserviceaccounts",
		}, key.Name)
	}

	f.msa.DeepCopyInto(msa)
	return nil
}

func (f fakeCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	panic("implement me")
}

func (f fakeCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
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

func (f fakeCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption) (cache.Informer, error) {
	panic("implement me")
}

func (f fakeCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	panic("implement me")
}

type managedServiceAccountBuilder struct {
	msa *authv1beta1.ManagedServiceAccount
}

func newManagedServiceAccount(namespace, name string) *managedServiceAccountBuilder {
	return &managedServiceAccountBuilder{
		msa: &authv1beta1.ManagedServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		},
	}
}

func (b *managedServiceAccountBuilder) build() *authv1beta1.ManagedServiceAccount {
	return b.msa
}

func (b *managedServiceAccountBuilder) withTTLSecondsAfterCreation(createTime time.Time, ttl int32) *managedServiceAccountBuilder {
	b.msa.CreationTimestamp = metav1.NewTime(createTime)
	b.msa.Spec.TTLSecondsAfterCreation = &ttl
	return b
}
