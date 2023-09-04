package controller

import (
	"context"
	"os"

	"github.com/pkg/errors"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"open-cluster-management.io/managed-serviceaccount/pkg/controllers/event"
)

var _ reconcile.Reconciler = &TokenReconciler{}

type TokenReconciler struct {
	cache.Cache
	HubClient         client.Client
	HubNativeClient   kubernetes.Interface
	SpokeNativeClient kubernetes.Interface
	SpokeClientConfig *rest.Config
	SpokeNamespace    string
	ClusterName       string
	SpokeCache        cache.Cache
}

// SetupWithManager sets up the controller with the Manager.
func (r *TokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authv1beta1.ManagedServiceAccount{}).
		Watches(
			&corev1.Secret{},
			event.NewSecretEventHandler(),
		).
		WatchesRawSource(
			source.Kind(
				r.SpokeCache,
				&corev1.ServiceAccount{},
			),
			event.NewServiceAccountEventHandler(r.ClusterName),
		).
		Complete(r)
}

func (r *TokenReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Start reconciling")
	managed := &authv1beta1.ManagedServiceAccount{}

	if err := r.Cache.Get(ctx, request.NamespacedName, managed); err != nil {
		if !apierrors.IsNotFound(err) {
			//fail to get managed-serviceaccount, requeue
			return reconcile.Result{}, errors.Wrapf(err, "fail to get managed serviceaccount")
		}

		sai := r.SpokeNativeClient.CoreV1().ServiceAccounts(r.SpokeNamespace)
		if err := sai.Delete(ctx, request.Name, metav1.DeleteOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				//fail to delete related serviceaccount, requeue
				return reconcile.Result{}, errors.Wrapf(err, "fail to delete related serviceaccount")
			}
		}

		logger.Info("Both ManagedServiceAccount and related ServiceAccount does not exist")
		return reconcile.Result{}, nil
	}

	if err := r.ensureServiceAccount(managed); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to ensure service account")
	}

	secretExists := true
	currentTokenSecret := &corev1.Secret{}
	if err := r.HubClient.Get(context.TODO(), types.NamespacedName{
		Namespace: managed.Namespace,
		Name:      managed.Name,
	}, currentTokenSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, errors.Wrapf(err, "failed to read current token secret from hub cluster")
		}
		secretExists = false
		currentTokenSecret = nil
	}

	if shouldCreate, err := r.isSoonExpiring(managed, currentTokenSecret); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to make a decision on token creation")
	} else if secretExists && !shouldCreate {
		logger.Info("Skipped creating token")
		return reconcile.Result{}, nil
	}

	token, expiring, err := r.createToken(managed)
	if err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to request token for service-account")
	}

	caData := r.SpokeClientConfig.CAData
	if len(caData) == 0 {
		var err error
		caData, err = os.ReadFile(r.SpokeClientConfig.CAFile)
		if err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "failed to read CA data from file")
		}
	}

	tokenSecret := buildSecret(managed, caData, []byte(token))
	if secretExists {
		currentTokenSecret.Data = tokenSecret.Data
		if err := r.HubClient.Update(context.TODO(), currentTokenSecret); err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "failed to update the token secret")
		}
	} else {
		if err := r.HubClient.Create(context.TODO(), tokenSecret); err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "failed to create the token secret")
		}
	}

	now := metav1.Now()
	conditions := []metav1.Condition{
		{
			Type:               authv1beta1.ConditionTypeTokenReported,
			Status:             metav1.ConditionTrue,
			Reason:             "TokenReported",
			LastTransitionTime: now,
		},
	}
	if !secretExists {
		conditions = append(conditions, metav1.Condition{
			Type:               authv1beta1.ConditionTypeSecretCreated,
			Status:             metav1.ConditionTrue,
			Reason:             "SecretCreated",
			LastTransitionTime: now,
		})
	}
	status := authv1beta1.ManagedServiceAccountStatus{
		Conditions:          mergeConditions(managed.Status.Conditions, conditions),
		ExpirationTimestamp: &expiring,
		TokenSecretRef: &authv1beta1.SecretRef{
			Name:                 managed.Name,
			LastRefreshTimestamp: now,
		},
	}

	munged := managed.DeepCopy()
	munged.Status = status
	if err := r.HubClient.Status().Update(context.TODO(), munged); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to update status")
	}

	logger.Info("Refreshed token")
	return reconcile.Result{}, nil
}

func (r *TokenReconciler) ensureServiceAccount(managed *authv1beta1.ManagedServiceAccount) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.SpokeNamespace,
			Name:      managed.Name,
			Labels: map[string]string{
				common.LabelKeyIsManagedServiceAccount: "true",
			},
		},
	}
	if _, err := r.SpokeNativeClient.CoreV1().
		ServiceAccounts(r.SpokeNamespace).
		Create(context.TODO(), sa, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return errors.Wrapf(err, "failed ensuring service account")
		}
	}
	return nil
}

func (r *TokenReconciler) createToken(managed *authv1beta1.ManagedServiceAccount) (string, metav1.Time, error) {
	var expirationSec = int64(managed.Spec.Rotation.Validity.Seconds())
	tr, err := r.SpokeNativeClient.CoreV1().ServiceAccounts(r.SpokeNamespace).
		CreateToken(context.TODO(), managed.Name, &authv1.TokenRequest{
			Spec: authv1.TokenRequestSpec{
				ExpirationSeconds: &expirationSec,
			},
		}, metav1.CreateOptions{})
	if err != nil {
		return "", metav1.Time{}, err
	}
	return tr.Status.Token, tr.Status.ExpirationTimestamp, nil
}

func buildSecret(managed *authv1beta1.ManagedServiceAccount, caData, tokenData []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: managed.Namespace,
			Name:      managed.Name,
			Labels: map[string]string{
				common.LabelKeyIsManagedServiceAccount: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: authv1beta1.GroupVersion.String(),
					Kind:       "ManagedServiceAccount",
					Name:       managed.Name,
					UID:        managed.UID,
				},
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			corev1.ServiceAccountRootCAKey: caData,
			corev1.ServiceAccountTokenKey:  tokenData,
		},
	}
}

func (r *TokenReconciler) isSoonExpiring(managed *authv1beta1.ManagedServiceAccount, tokenSecret *corev1.Secret) (bool, error) {
	if managed.Status.TokenSecretRef == nil || tokenSecret == nil {
		return true, nil
	}

	// check if the token should be refreshed
	// the token will not be rotated unless its remaining lifetime is less
	// than 20% of its rotation validity
	now := metav1.Now()
	refreshThreshold := managed.Spec.Rotation.Validity.Duration / 5 * 1
	lifetime := managed.Status.ExpirationTimestamp.Sub(now.Time)
	if lifetime < refreshThreshold {
		return true, nil
	}

	// check if the token is valid or not
	tokenReview := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{
			Token: string(tokenSecret.Data[corev1.ServiceAccountTokenKey]),
		},
	}
	tr, err := r.SpokeNativeClient.AuthenticationV1().TokenReviews().Create(
		context.TODO(), tokenReview, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}

	return !tr.Status.Authenticated, nil
}

func mergeConditions(old, new []metav1.Condition) []metav1.Condition {
	for _, cnew := range new {
		cnew := cnew
		found := false
		idx := 0
		for i, cold := range old {
			if cold.Type == cnew.Type {
				found = true
				idx = i
			}
		}
		if found {
			old[idx] = cnew
		} else {
			old = append(old, cnew)
		}
	}
	return old
}
