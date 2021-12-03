package controller

import (
	"context"
	"io/ioutil"

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

	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
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
}

// SetupWithManager sets up the controller with the Manager.
func (r *TokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authv1alpha1.ManagedServiceAccount{}).
		Watches(&source.Kind{Type: &corev1.Secret{}}, event.NewSecretEventHandler()).
		Complete(r)
}

func (r *TokenReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Start reconciling")
	managed := &authv1alpha1.ManagedServiceAccount{}
	if err := r.Cache.Get(ctx, request.NamespacedName, managed); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, errors.Wrapf(err, "no such managed service account")
		}
		logger.Info("No such resource")
		return reconcile.Result{}, nil
	}

	if err := r.ensureServiceAccount(managed); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to ensure service account")
	}

	if !shouldCreateToken(managed) {
		logger.Info("Skipped creating token")
		return reconcile.Result{}, nil
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
	}

	token, expiring, err := r.createToken(managed)
	if err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to request token for service-account")
	}

	caData := r.SpokeClientConfig.CAData
	if len(caData) == 0 {
		var err error
		caData, err = ioutil.ReadFile(r.SpokeClientConfig.CAFile)
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
			Type:               authv1alpha1.ConditionTypeTokenReported,
			Status:             metav1.ConditionTrue,
			Reason:             "TokenReported",
			LastTransitionTime: now,
		},
	}
	if !secretExists {
		conditions = append(conditions, metav1.Condition{
			Type:               authv1alpha1.ConditionTypeSecretCreated,
			Status:             metav1.ConditionTrue,
			Reason:             "SecretCreated",
			LastTransitionTime: now,
		})
	}
	status := authv1alpha1.ManagedServiceAccountStatus{
		Conditions:          mergeConditions(managed.Status.Conditions, conditions),
		ExpirationTimestamp: &expiring,
		TokenSecretRef: &authv1alpha1.SecretRef{
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

func (r *TokenReconciler) ensureServiceAccount(managed *authv1alpha1.ManagedServiceAccount) error {
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

func (r *TokenReconciler) createToken(managed *authv1alpha1.ManagedServiceAccount) (string, metav1.Time, error) {
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

func buildSecret(managed *authv1alpha1.ManagedServiceAccount, caData, tokenData []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: managed.Namespace,
			Name:      managed.Name,
			Labels: map[string]string{
				common.LabelKeyIsManagedServiceAccount: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: authv1alpha1.GroupVersion.String(),
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

func shouldCreateToken(managed *authv1alpha1.ManagedServiceAccount) bool {
	if managed.Status.TokenSecretRef == nil {
		return true
	}
	now := metav1.Now()
	refreshThreshold := managed.Spec.Rotation.Validity.Duration / 5 * 4
	lifetime := managed.Status.ExpirationTimestamp.Sub(now.Time)
	if lifetime < refreshThreshold {
		return true
	}
	return false
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
