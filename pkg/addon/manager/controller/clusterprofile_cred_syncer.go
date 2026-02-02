package controller

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	cpv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const (
	// LabelKeySyncedFrom is set on synced secrets to identify the source ManagedServiceAccount (format: "<namespace>-<name>")
	LabelKeySyncedFrom = "authentication.open-cluster-management.io/synced-from"
	// LabelKeyClusterProfileSync marks ManagedServiceAccounts to sync the corresponding credentials synced to ClusterProfile namespace
	LabelKeyClusterProfileSync = "authentication.open-cluster-management.io/sync-to-clusterprofile"
)

const ClusterProfileManagerName = "open-cluster-management"

var _ reconcile.Reconciler = &ClusterProfileCredSyncer{}

var logger = ctrl.Log.WithName("ClusterProfileCredSyncer")

type ClusterProfileCredSyncer struct {
	cache.Cache
	HubClient client.Client
}

func NewClusterProfileCredSyncer(cache cache.Cache, hubClient client.Client) *ClusterProfileCredSyncer {
	return &ClusterProfileCredSyncer{
		Cache:     cache,
		HubClient: hubClient,
	}
}

// SetupWithManager sets up the clusterProfileCredSyncer with the manager.
func (r *ClusterProfileCredSyncer) SetupWithManager(mgr ctrl.Manager) error {
	// Predicate to filter only ClusterProfiles managed by this controller
	cpFilter := func(obj client.Object) bool {
		if cp, ok := obj.(*cpv1alpha1.ClusterProfile); ok {
			if cp.Labels[cpv1alpha1.LabelClusterManagerKey] != ClusterProfileManagerName {
				return false
			}
			_, ok := cp.Labels[clusterv1.ClusterNameLabelKey]
			return ok
		}
		return false
	}

	// Predicate to filter only ManagedServiceAccounts with the sync label
	msaFilter := func(obj client.Object) bool {
		if msa, ok := obj.(*authv1beta1.ManagedServiceAccount); ok {
			return msa.Labels[LabelKeyClusterProfileSync] == "true"
		}
		return false
	}

	// Predicate to filter only token secrets with the required label
	secretFilter := func(obj client.Object) bool {
		if secret, ok := obj.(*corev1.Secret); ok {
			return secret.Labels[common.LabelKeyIsManagedServiceAccount] == "true"
		}
		return false
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&cpv1alpha1.ClusterProfile{}, builder.WithPredicates(predicate.NewPredicateFuncs(cpFilter))).
		Watches(
			&authv1beta1.ManagedServiceAccount{},
			handler.EnqueueRequestsFromMapFunc(r.mapManagedServiceAccountToClusterProfile),
			builder.WithPredicates(predicate.NewPredicateFuncs(msaFilter)),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapTokenSecretToClusterProfile),
			builder.WithPredicates(predicate.NewPredicateFuncs(secretFilter)),
		).
		Complete(r)
}

// mapManagedServiceAccountToClusterProfile maps managedserviceaccount events to the corresponding clusterprofile
func (r *ClusterProfileCredSyncer) mapManagedServiceAccountToClusterProfile(ctx context.Context, obj client.Object) []reconcile.Request {
	// when a managedserviceaccount changes, reconcile the corresponding clusterprofile
	// clusterprofile name = managedserviceaccount namespace
	msa, ok := obj.(*authv1beta1.ManagedServiceAccount)
	if !ok {
		logger.Error(fmt.Errorf("unexpected object type"), "expected managedserviceaccount")
		return []reconcile.Request{}
	}

	// Find all ClusterProfiles with name matching the managedserviceaccount namespace
	cpList := &cpv1alpha1.ClusterProfileList{}
	if err := r.Cache.List(ctx, cpList); err != nil {
		logger.Error(err, "failed to list clusterprofiles")
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, cp := range cpList.Items {
		if cp.Name == msa.Namespace {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: cp.Namespace,
					Name:      cp.Name,
				},
			})
		}
	}

	return requests
}

// mapTokenSecretToClusterProfile maps token secret events to the corresponding clusterprofile
func (r *ClusterProfileCredSyncer) mapTokenSecretToClusterProfile(ctx context.Context, obj client.Object) []reconcile.Request {
	// when a token secret changes, reconcile the corresponding clusterprofile
	// clusterprofile name = secret namespace
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		logger.Error(fmt.Errorf("unexpected object type"), "expected secret")
		return []reconcile.Request{}
	}

	// Find all ClusterProfiles with name matching the secret namespace
	cpList := &cpv1alpha1.ClusterProfileList{}
	if err := r.Cache.List(ctx, cpList); err != nil {
		logger.Error(err, "failed to list clusterprofiles")
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, cp := range cpList.Items {
		if cp.Name == secret.Namespace {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: cp.Namespace,
					Name:      cp.Name,
				},
			})
		}
	}

	return requests
}

func (r *ClusterProfileCredSyncer) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger.Info("Start reconcile", "namespace", req.Namespace, "name", req.Name)

	// get the clusterprofile
	cp := &cpv1alpha1.ClusterProfile{}
	if err := r.Cache.Get(ctx, req.NamespacedName, cp); err != nil {
		if apierrors.IsNotFound(err) {
			// clusterprofile is deleted, owner reference will handle cleanup automatically
			logger.Info("ClusterProfile not found, secrets will be cleaned up by garbage collection")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, errors.Wrapf(err, "failed to get clusterprofile")
	}

	// List managedserviceaccount only in the namespace matching the clusterprofile name
	// and with the required sync label
	msaList := &authv1beta1.ManagedServiceAccountList{}
	if err := r.Cache.List(ctx, msaList,
		client.InNamespace(cp.Name),
		client.MatchingLabels{LabelKeyClusterProfileSync: "true"},
	); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to list managedserviceaccounts in namespace %s", cp.Name)
	}

	// Sync credentials from managedserviceaccounts to clusterprofile namespace
	var errs []error
	for _, msa := range msaList.Items {
		if err := r.syncCreds(ctx, &msa, cp); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to sync credential for msa %s/%s", msa.Namespace, msa.Name))
		}
	}

	// Clean up synced credentials that no longer have corresponding managedserviceaccounts
	if err := r.cleanupOrphanedCreds(ctx, cp, msaList.Items); err != nil {
		errs = append(errs, errors.Wrapf(err, "failed to cleanup orphaned credentials"))
	}

	logger.Info("Reconcile completed", "namespace", req.Namespace, "name", req.Name)
	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// syncCreds syncs the credential secret from a managedserviceaccount to the clusterprofile namespace
func (r *ClusterProfileCredSyncer) syncCreds(ctx context.Context, msa *authv1beta1.ManagedServiceAccount, cp *cpv1alpha1.ClusterProfile) error {
	// Check if the managedserviceaccount has a token secret
	if msa.Status.TokenSecretRef == nil {
		logger.V(4).Info("ManagedServiceAccount has no token secret yet", "msa", msa.Name, "namespace", msa.Namespace)
		return nil
	}

	// Get the source secret using TokenSecretRef
	sourceSecret := &corev1.Secret{}
	sourceSecretName := types.NamespacedName{
		Namespace: msa.Namespace,
		Name:      msa.Status.TokenSecretRef.Name,
	}
	if err := r.Cache.Get(ctx, sourceSecretName, sourceSecret); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(4).Info("Source secret not found", "secret", sourceSecretName.String())
			return nil
		}
		return errors.Wrapf(err, "failed to get source secret %s", sourceSecretName.String())
	}

	// Create the synced credential secret name: <namespace>-<name>
	syncedCredName := fmt.Sprintf("%s-%s", msa.Namespace, msa.Name)

	// Get or create the synced credential secret
	syncedCred := &corev1.Secret{}
	syncedCredNamespacedName := types.NamespacedName{
		Namespace: cp.Namespace,
		Name:      syncedCredName,
	}

	err := r.Cache.Get(ctx, syncedCredNamespacedName, syncedCred)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to get synced credential %s", syncedCredNamespacedName.String())
		}
		// Secret doesn't exist, create it
		syncedCred = r.buildSyncedCred(msa, cp, syncedCredName, sourceSecret)
		if err := r.HubClient.Create(ctx, syncedCred); err != nil {
			return errors.Wrapf(err, "failed to create synced credential %s", syncedCredNamespacedName.String())
		}
		logger.Info("Created synced credential", "secret", syncedCredNamespacedName.String())
		return nil
	}

	// Secret exists, update it if needed
	updatedCred := r.buildSyncedCred(msa, cp, syncedCredName, sourceSecret)
	if secretNeedsUpdate(syncedCred, updatedCred) {
		syncedCred.Data = updatedCred.Data
		syncedCred.Labels = updatedCred.Labels
		syncedCred.OwnerReferences = updatedCred.OwnerReferences
		if err := r.HubClient.Update(ctx, syncedCred); err != nil {
			return errors.Wrapf(err, "failed to update synced credential %s", syncedCredNamespacedName.String())
		}
		logger.Info("Updated synced credential", "secret", syncedCredNamespacedName.String())
	}

	return nil
}

// buildSyncedCred builds a synced credential secret from the source secret
func (r *ClusterProfileCredSyncer) buildSyncedCred(msa *authv1beta1.ManagedServiceAccount, cp *cpv1alpha1.ClusterProfile, syncedCredName string, sourceSecret *corev1.Secret) *corev1.Secret {
	// Copy all data from source secret
	dataCopy := make(map[string][]byte, len(sourceSecret.Data))
	for k, v := range sourceSecret.Data {
		dataCopy[k] = v
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cp.Namespace,
			Name:      syncedCredName,
			Labels: map[string]string{
				LabelKeySyncedFrom: fmt.Sprintf("%s-%s", msa.Namespace, msa.Name),
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: cpv1alpha1.GroupVersion.String(),
					Kind:       cpv1alpha1.Kind,
					Name:       cp.Name,
					UID:        cp.UID,
					Controller: ptr(true),
				},
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: dataCopy,
	}
}

func ptr(b bool) *bool {
	return &b
}

// secretNeedsUpdate checks if the synced secret needs to be updated
func secretNeedsUpdate(current, desired *corev1.Secret) bool {
	// Check if data has changed
	if !dataEqual(current.Data, desired.Data) {
		return true
	}

	// Check if labels have changed
	if len(current.Labels) == 0 {
		return len(desired.Labels) > 0
	}
	for k, v := range desired.Labels {
		if current.Labels[k] != v {
			return true
		}
	}

	return false
}

// dataEqual checks if two secret data maps are equal
func dataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || string(v) != string(bv) {
			return false
		}
	}
	return true
}

// isOwnedByClusterProfile checks if a secret is owned by the given clusterprofile
func isOwnedByClusterProfile(secret *corev1.Secret, cp *cpv1alpha1.ClusterProfile) bool {
	for _, ownerRef := range secret.OwnerReferences {
		if ownerRef.UID == cp.UID {
			return true
		}
	}
	return false
}

// cleanupOrphanedCreds removes synced credentials that no longer have corresponding managedserviceaccounts
func (r *ClusterProfileCredSyncer) cleanupOrphanedCreds(ctx context.Context, cp *cpv1alpha1.ClusterProfile, msaList []authv1beta1.ManagedServiceAccount) error {
	// List only secrets with LabelKeySyncedFrom label in the clusterprofile namespace
	secretList := &corev1.SecretList{}
	if err := r.Cache.List(ctx, secretList,
		client.InNamespace(cp.Namespace),
		client.HasLabels{LabelKeySyncedFrom},
	); err != nil {
		return errors.Wrapf(err, "failed to list secrets in namespace %s", cp.Namespace)
	}

	// Build a set of valid managedserviceaccount identifiers
	validMSAs := make(map[string]bool)
	for _, msa := range msaList {
		validMSAs[fmt.Sprintf("%s-%s", msa.Namespace, msa.Name)] = true
	}

	// Delete credentials that don't have corresponding managedserviceaccount
	// Only clean up secrets that are owned by this clusterprofile
	var errs []error
	for _, secret := range secretList.Items {
		// Check if this secret is owned by the current clusterprofile
		if !isOwnedByClusterProfile(&secret, cp) {
			continue
		}

		syncedFrom := secret.Labels[LabelKeySyncedFrom]
		if !validMSAs[syncedFrom] {
			logger.Info("Deleting orphaned synced credential", "secret", secret.Name, "syncedFrom", syncedFrom)
			if err := r.HubClient.Delete(ctx, &secret); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, errors.Wrapf(err, "failed to delete orphaned secret %s", secret.Name))
			}
		}
	}

	return utilerrors.NewAggregate(errs)
}
