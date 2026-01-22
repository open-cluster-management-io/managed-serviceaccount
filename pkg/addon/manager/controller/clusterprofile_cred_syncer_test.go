package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	cpv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestClusterProfileCredSyncerReconcile(t *testing.T) {
	testCases := []struct {
		name            string
		clusterProfile  *cpv1alpha1.ClusterProfile
		msaList         []authv1beta1.ManagedServiceAccount
		existingSecrets []corev1.Secret
		validateFunc    func(t *testing.T, hubClient client.Client)
	}{
		{
			name:           "ClusterProfile not found - owner reference handles cleanup",
			clusterProfile: nil,
			existingSecrets: []corev1.Secret{
				*newSecret(ClusterProfileNamespace, "cluster1-msa1").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa1").
					build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				// With owner references, Kubernetes GC will handle cleanup automatically
				// The reconciler just logs and returns nil
			},
		},
		{
			name: "Sync credentials from ManagedServiceAccounts to ClusterProfile namespace",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster1").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				*newManagedServiceAccountWithToken("cluster1", "msa1").build(),
				*newManagedServiceAccountWithToken("cluster1", "msa2").build(),
			},
			existingSecrets: []corev1.Secret{
				*newTokenSecret("cluster1", "msa1").build(),
				*newTokenSecret("cluster1", "msa2").build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				// Verify synced credentials exist in ClusterProfile namespace
				cred1 := &corev1.Secret{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa1",
				}, cred1)
				assert.NoError(t, err, "synced credential cluster1-msa1 should exist")
				assert.Equal(t, "cluster1-msa1", cred1.Labels[LabelKeySyncedFrom])
				assert.Equal(t, "true", cred1.Labels[LabelKeyClusterProfileCreds])
				// Verify all data is copied
				assert.NotEmpty(t, cred1.Data[corev1.ServiceAccountTokenKey])
				assert.NotEmpty(t, cred1.Data[corev1.ServiceAccountRootCAKey])
				// Verify owner reference is set
				assert.Len(t, cred1.OwnerReferences, 1)
				assert.Equal(t, "cluster1", cred1.OwnerReferences[0].Name)
				assert.True(t, *cred1.OwnerReferences[0].Controller)

				cred2 := &corev1.Secret{}
				err = hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa2",
				}, cred2)
				assert.NoError(t, err, "synced credential cluster1-msa2 should exist")
				assert.Equal(t, "cluster1-msa2", cred2.Labels[LabelKeySyncedFrom])
				assert.Equal(t, "true", cred2.Labels[LabelKeyClusterProfileCreds])
				// Verify owner reference is set
				assert.Len(t, cred2.OwnerReferences, 1)
				assert.Equal(t, "cluster1", cred2.OwnerReferences[0].Name)
			},
		},
		{
			name: "Update existing synced credential when token changes",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster1").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				*newManagedServiceAccountWithToken("cluster1", "msa1").build(),
			},
			existingSecrets: []corev1.Secret{
				*newTokenSecret("cluster1", "msa1").
					withToken([]byte("new-token-value")).
					build(),
				*newSecret(ClusterProfileNamespace, "cluster1-msa1").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa1").
					withData(corev1.ServiceAccountTokenKey, []byte("old-token-value")).
					build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				cred := &corev1.Secret{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa1",
				}, cred)
				assert.NoError(t, err)
				assert.Equal(t, []byte("new-token-value"), cred.Data[corev1.ServiceAccountTokenKey])
				// Verify owner reference is updated
				assert.Len(t, cred.OwnerReferences, 1)
				assert.Equal(t, "cluster1", cred.OwnerReferences[0].Name)
			},
		},
		{
			name: "Cleanup orphaned credentials when ManagedServiceAccount is deleted",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster1").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				*newManagedServiceAccountWithToken("cluster1", "msa1").build(),
			},
			existingSecrets: []corev1.Secret{
				*newTokenSecret("cluster1", "msa1").build(),
				*newSecret(ClusterProfileNamespace, "cluster1-msa1").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa1").
					withOwnerReference(newClusterProfile(ClusterProfileNamespace, "cluster1").build()).
					build(),
				// Orphaned credential from deleted ManagedServiceAccount
				*newSecret(ClusterProfileNamespace, "cluster1-deleted-msa").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-deleted-msa").
					withOwnerReference(newClusterProfile(ClusterProfileNamespace, "cluster1").build()).
					build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				// Valid synced credential should exist
				cred := &corev1.Secret{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa1",
				}, cred)
				assert.NoError(t, err, "valid synced credential should exist")

				// Orphaned credential should be deleted
				orphanedCred := &corev1.Secret{}
				err = hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-deleted-msa",
				}, orphanedCred)
				assert.True(t, apierrors.IsNotFound(err), "orphaned credential should be deleted")
			},
		},
		{
			name: "ManagedServiceAccount without token secret - no sync",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster1").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				// ManagedServiceAccount without token secret ref
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "msa-no-token",
						Namespace: "cluster1",
					},
					Status: authv1beta1.ManagedServiceAccountStatus{
						TokenSecretRef: nil,
					},
				},
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				secretList := &corev1.SecretList{}
				err := hubClient.List(context.TODO(), secretList, client.InNamespace(ClusterProfileNamespace))
				assert.NoError(t, err)
				assert.Equal(t, 0, len(secretList.Items), "no credentials should be synced")
			},
		},
		{
			name: "Do not delete synced credentials owned by other ClusterProfiles",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster2").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				*newManagedServiceAccountWithToken("cluster2", "msa2").build(),
			},
			existingSecrets: []corev1.Secret{
				// Token secret for cluster2/msa2
				*newTokenSecret("cluster2", "msa2").build(),
				// Synced credential owned by cluster2 (current ClusterProfile)
				*newSecret(ClusterProfileNamespace, "cluster2-msa2").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster2-msa2").
					withOwnerReference(newClusterProfile(ClusterProfileNamespace, "cluster2").build()).
					build(),
				// Synced credential owned by cluster1 (different ClusterProfile)
				// This should NOT be deleted when reconciling cluster2
				*newSecret(ClusterProfileNamespace, "cluster1-msa1").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa1").
					withOwnerReference(newClusterProfile(ClusterProfileNamespace, "cluster1").build()).
					build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				// Synced credential for cluster2-msa2 should exist
				cred2 := &corev1.Secret{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster2-msa2",
				}, cred2)
				assert.NoError(t, err, "cluster2-msa2 should exist")

				// Synced credential for cluster1-msa1 should still exist
				// (not deleted by cluster2 reconciliation)
				cred1 := &corev1.Secret{}
				err = hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa1",
				}, cred1)
				assert.NoError(t, err, "cluster1-msa1 should NOT be deleted by cluster2 reconciliation")
				assert.Equal(t, "cluster1-msa1", cred1.Labels[LabelKeySyncedFrom])
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create fake client scheme with all required types
			testscheme := runtime.NewScheme()
			authv1beta1.AddToScheme(testscheme)
			corev1.AddToScheme(testscheme)
			cpv1alpha1.AddToScheme(testscheme)

			// Build runtime objects
			objs := []runtime.Object{}
			if tc.clusterProfile != nil {
				objs = append(objs, tc.clusterProfile)
			}
			for i := range tc.msaList {
				objs = append(objs, &tc.msaList[i])
			}
			for i := range tc.existingSecrets {
				objs = append(objs, &tc.existingSecrets[i])
			}

			hubClient := fake.NewClientBuilder().
				WithScheme(testscheme).
				WithRuntimeObjects(objs...).
				Build()

			reconciler := NewClusterProfileCredSyncer(
				&clusterProfileFakeCache{
					clusterProfile: tc.clusterProfile,
					msaList:        tc.msaList,
					secrets:        tc.existingSecrets,
				},
				hubClient,
			)

			// Determine the reconcile request based on cluster profile
			reqName := "cluster1"
			reqNamespace := ClusterProfileNamespace
			if tc.clusterProfile != nil {
				reqName = tc.clusterProfile.Name
				reqNamespace = tc.clusterProfile.Namespace
			}

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      reqName,
					Namespace: reqNamespace,
				},
			})

			assert.NoError(t, err)

			if tc.validateFunc != nil {
				tc.validateFunc(t, hubClient)
			}
		})
	}
}

// clusterProfileFakeCache is a fake cache implementation for testing
type clusterProfileFakeCache struct {
	clusterProfile *cpv1alpha1.ClusterProfile
	msaList        []authv1beta1.ManagedServiceAccount
	secrets        []corev1.Secret
}

func (f *clusterProfileFakeCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch v := obj.(type) {
	case *cpv1alpha1.ClusterProfile:
		if f.clusterProfile == nil {
			return apierrors.NewNotFound(schema.GroupResource{
				Group:    cpv1alpha1.GroupVersion.Group,
				Resource: "clusterprofiles",
			}, key.Name)
		}
		f.clusterProfile.DeepCopyInto(v)
		return nil
	case *corev1.Secret:
		for _, secret := range f.secrets {
			if secret.Namespace == key.Namespace && secret.Name == key.Name {
				secret.DeepCopyInto(v)
				return nil
			}
		}
		return apierrors.NewNotFound(schema.GroupResource{
			Group:    "",
			Resource: "secrets",
		}, key.Name)
	}
	return fmt.Errorf("unsupported type: %T", obj)
}

func (f *clusterProfileFakeCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch v := list.(type) {
	case *authv1beta1.ManagedServiceAccountList:
		// Filter by namespace if specified
		namespace := ""
		for _, opt := range opts {
			if nsOpt, ok := opt.(client.InNamespace); ok {
				namespace = string(nsOpt)
			}
		}

		filteredMSAs := []authv1beta1.ManagedServiceAccount{}
		for _, msa := range f.msaList {
			if namespace == "" || msa.Namespace == namespace {
				filteredMSAs = append(filteredMSAs, msa)
			}
		}
		v.Items = filteredMSAs
		return nil
	case *corev1.SecretList:
		// Filter by namespace and labels if specified
		namespace := ""
		matchLabels := map[string]string{}
		for _, opt := range opts {
			if nsOpt, ok := opt.(client.InNamespace); ok {
				namespace = string(nsOpt)
			}
			if labelOpt, ok := opt.(client.MatchingLabels); ok {
				matchLabels = labelOpt
			}
		}

		filteredSecrets := []corev1.Secret{}
		for _, secret := range f.secrets {
			// Filter by namespace
			if namespace != "" && secret.Namespace != namespace {
				continue
			}
			// Filter by labels
			matchesLabels := true
			for k, v := range matchLabels {
				if secret.Labels[k] != v {
					matchesLabels = false
					break
				}
			}
			if matchesLabels {
				filteredSecrets = append(filteredSecrets, secret)
			}
		}
		v.Items = filteredSecrets
		return nil
	}
	return fmt.Errorf("unsupported list type: %T", list)
}

func (f *clusterProfileFakeCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	panic("implement me")
}

func (f *clusterProfileFakeCache) Start(ctx context.Context) error {
	panic("implement me")
}

func (f *clusterProfileFakeCache) WaitForCacheSync(ctx context.Context) bool {
	panic("implement me")
}

func (f *clusterProfileFakeCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	panic("implement me")
}

func (f *clusterProfileFakeCache) Set(key string, responseBytes []byte) {
	panic("implement me")
}

func (f *clusterProfileFakeCache) Delete(key string) {
	panic("implement me")
}

func (f *clusterProfileFakeCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption) (cache.Informer, error) {
	panic("implement me")
}

func (f *clusterProfileFakeCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	panic("implement me")
}

// Test helpers and builders

type clusterProfileBuilder struct {
	cp *cpv1alpha1.ClusterProfile
}

func newClusterProfile(namespace, name string) *clusterProfileBuilder {
	return &clusterProfileBuilder{
		cp: &cpv1alpha1.ClusterProfile{
			TypeMeta: metav1.TypeMeta{
				APIVersion: cpv1alpha1.GroupVersion.String(),
				Kind:       "ClusterProfile",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				UID:       types.UID(fmt.Sprintf("test-uid-%s", name)),
			},
			Spec: cpv1alpha1.ClusterProfileSpec{
				ClusterManager: cpv1alpha1.ClusterManager{
					Name: "test-manager",
				},
			},
		},
	}
}

func (b *clusterProfileBuilder) build() *cpv1alpha1.ClusterProfile {
	return b.cp
}

type msaBuilder struct {
	msa *authv1beta1.ManagedServiceAccount
}

func newManagedServiceAccountWithToken(namespace, name string) *msaBuilder {
	return &msaBuilder{
		msa: &authv1beta1.ManagedServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Status: authv1beta1.ManagedServiceAccountStatus{
				TokenSecretRef: &authv1beta1.SecretRef{
					Name: name,
				},
			},
		},
	}
}

func (b *msaBuilder) build() *authv1beta1.ManagedServiceAccount {
	return b.msa
}

type secretBuilder struct {
	secret *corev1.Secret
}

func newSecret(namespace, name string) *secretBuilder {
	return &secretBuilder{
		secret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{},
		},
	}
}

func newTokenSecret(namespace, name string) *secretBuilder {
	return &secretBuilder{
		secret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					common.LabelKeyIsManagedServiceAccount: "true",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				corev1.ServiceAccountTokenKey:  []byte("test-token"),
				corev1.ServiceAccountRootCAKey: []byte("test-ca"),
			},
		},
	}
}

func (b *secretBuilder) withLabel(key, value string) *secretBuilder {
	if b.secret.Labels == nil {
		b.secret.Labels = map[string]string{}
	}
	b.secret.Labels[key] = value
	return b
}

func (b *secretBuilder) withData(key string, value []byte) *secretBuilder {
	if b.secret.Data == nil {
		b.secret.Data = map[string][]byte{}
	}
	b.secret.Data[key] = value
	return b
}

func (b *secretBuilder) withToken(token []byte) *secretBuilder {
	if b.secret.Data == nil {
		b.secret.Data = map[string][]byte{}
	}
	b.secret.Data[corev1.ServiceAccountTokenKey] = token
	b.secret.Data[corev1.ServiceAccountRootCAKey] = []byte("test-ca")
	return b
}

func (b *secretBuilder) withOwnerReference(cp *cpv1alpha1.ClusterProfile) *secretBuilder {
	b.secret.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: cpv1alpha1.GroupVersion.String(),
			Kind:       cpv1alpha1.Kind,
			Name:       cp.Name,
			UID:        cp.UID,
			Controller: ptr(true),
		},
	}
	return b
}

func (b *secretBuilder) build() *corev1.Secret {
	return b.secret
}

func TestMapManagedServiceAccountToClusterProfile(t *testing.T) {
	reconciler := &ClusterProfileCredSyncer{}

	msa := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-msa",
			Namespace: "cluster1",
		},
	}

	requests := reconciler.mapManagedServiceAccountToClusterProfile(context.Background(), msa)

	assert.Len(t, requests, 1)
	assert.Equal(t, ClusterProfileNamespace, requests[0].Namespace)
	assert.Equal(t, "cluster1", requests[0].Name)
}

func TestMapTokenSecretToClusterProfile(t *testing.T) {
	reconciler := &ClusterProfileCredSyncer{}

	testCases := []struct {
		name             string
		secret           client.Object
		expectedRequests int
		expectedName     string
	}{
		{
			name: "Token secret with label maps to cluster profile",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-token",
					Namespace: "cluster1",
					Labels: map[string]string{
						common.LabelKeyIsManagedServiceAccount: "true",
					},
				},
			},
			expectedRequests: 1,
			expectedName:     "cluster1",
		},
		{
			name: "Token secret without label still maps (predicate filters)",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-token",
					Namespace: "cluster2",
				},
			},
			expectedRequests: 1,
			expectedName:     "cluster2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			requests := reconciler.mapTokenSecretToClusterProfile(context.Background(), tc.secret)

			assert.Len(t, requests, tc.expectedRequests)
			if tc.expectedRequests > 0 {
				assert.Equal(t, ClusterProfileNamespace, requests[0].Namespace)
				assert.Equal(t, tc.expectedName, requests[0].Name)
			}
		})
	}
}

func TestTokenRotationTriggersSync(t *testing.T) {
	// This test verifies that when a token secret is updated (rotation),
	// the synced credential is updated accordingly
	testCases := []struct {
		name              string
		clusterProfile    *cpv1alpha1.ClusterProfile
		msaList           []authv1beta1.ManagedServiceAccount
		existingSecrets   []corev1.Secret
		updatedTokenValue []byte
		validateFunc      func(t *testing.T, hubClient client.Client)
	}{
		{
			name: "Token rotation updates synced credential",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster1").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				*newManagedServiceAccountWithToken("cluster1", "msa1").build(),
			},
			existingSecrets: []corev1.Secret{
				// Initial token secret with old token
				*newTokenSecret("cluster1", "msa1").
					withToken([]byte("rotated-token-value")).
					build(),
				// Existing synced credential with old token
				*newSecret(ClusterProfileNamespace, "cluster1-msa1").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa1").
					withData(corev1.ServiceAccountTokenKey, []byte("old-token-value")).
					withData(corev1.ServiceAccountRootCAKey, []byte("test-ca")).
					build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				// Verify the synced credential was updated with the new token
				cred := &corev1.Secret{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa1",
				}, cred)
				assert.NoError(t, err)
				assert.Equal(t, []byte("rotated-token-value"), cred.Data[corev1.ServiceAccountTokenKey],
					"synced credential should have the new rotated token")
				assert.Equal(t, []byte("test-ca"), cred.Data[corev1.ServiceAccountRootCAKey])
			},
		},
		{
			name: "Multiple token rotations sync correctly",
			clusterProfile: newClusterProfile(ClusterProfileNamespace, "cluster1").
				build(),
			msaList: []authv1beta1.ManagedServiceAccount{
				*newManagedServiceAccountWithToken("cluster1", "msa1").build(),
				*newManagedServiceAccountWithToken("cluster1", "msa2").build(),
			},
			existingSecrets: []corev1.Secret{
				// Token secret 1 rotated
				*newTokenSecret("cluster1", "msa1").
					withToken([]byte("new-token-1")).
					build(),
				// Token secret 2 rotated
				*newTokenSecret("cluster1", "msa2").
					withToken([]byte("new-token-2")).
					build(),
				// Existing synced credentials with old tokens
				*newSecret(ClusterProfileNamespace, "cluster1-msa1").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa1").
					withData(corev1.ServiceAccountTokenKey, []byte("old-token-1")).
					build(),
				*newSecret(ClusterProfileNamespace, "cluster1-msa2").
					withLabel(LabelKeyClusterProfileCreds, "true").
					withLabel(LabelKeySyncedFrom, "cluster1-msa2").
					withData(corev1.ServiceAccountTokenKey, []byte("old-token-2")).
					build(),
			},
			validateFunc: func(t *testing.T, hubClient client.Client) {
				// Verify both credentials were updated
				cred1 := &corev1.Secret{}
				err := hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa1",
				}, cred1)
				assert.NoError(t, err)
				assert.Equal(t, []byte("new-token-1"), cred1.Data[corev1.ServiceAccountTokenKey])

				cred2 := &corev1.Secret{}
				err = hubClient.Get(context.TODO(), types.NamespacedName{
					Namespace: ClusterProfileNamespace,
					Name:      "cluster1-msa2",
				}, cred2)
				assert.NoError(t, err)
				assert.Equal(t, []byte("new-token-2"), cred2.Data[corev1.ServiceAccountTokenKey])
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create fake client scheme with all required types
			testscheme := runtime.NewScheme()
			authv1beta1.AddToScheme(testscheme)
			corev1.AddToScheme(testscheme)
			cpv1alpha1.AddToScheme(testscheme)

			// Build runtime objects
			objs := []runtime.Object{}
			if tc.clusterProfile != nil {
				objs = append(objs, tc.clusterProfile)
			}
			for i := range tc.msaList {
				objs = append(objs, &tc.msaList[i])
			}
			for i := range tc.existingSecrets {
				objs = append(objs, &tc.existingSecrets[i])
			}

			hubClient := fake.NewClientBuilder().
				WithScheme(testscheme).
				WithRuntimeObjects(objs...).
				Build()

			reconciler := NewClusterProfileCredSyncer(
				&clusterProfileFakeCache{
					clusterProfile: tc.clusterProfile,
					msaList:        tc.msaList,
					secrets:        tc.existingSecrets,
				},
				hubClient,
			)

			// Reconcile (simulates the controller responding to token secret change)
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tc.clusterProfile.Name,
					Namespace: tc.clusterProfile.Namespace,
				},
			})

			assert.NoError(t, err)

			if tc.validateFunc != nil {
				tc.validateFunc(t, hubClient)
			}
		})
	}
}
