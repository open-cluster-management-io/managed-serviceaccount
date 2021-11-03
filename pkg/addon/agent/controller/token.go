package controller

import (
	"context"
	"k8s.io/client-go/rest"
	"time"

	"github.com/pkg/errors"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

func (r *TokenReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	managed := &authv1alpha1.ManagedServiceAccount{}
	if err := r.Cache.Get(ctx, request.NamespacedName, managed); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, errors.Wrapf(err, "no such managed service account")
		}
		return reconcile.Result{}, nil
	}

	if err := r.ensureServiceAccount(managed); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to ensure service account")
	}

	if !r.shouldCreateToken(managed) {
		return reconcile.Result{}, nil
	}

	token, expiring, err := r.createToken(managed)
	if err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to request token for service-account")
	}
	status := authv1alpha1.ManagedServiceAccountStatus{
		Token:               token,
		ExpirationTimestamp: &expiring,
		CACertificateData:   r.SpokeClientConfig.CAData,
	}

	munged := managed.DeepCopy()
	munged.Status = status
	if err := r.HubClient.Status().Update(context.TODO(), munged); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to update status")
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authv1alpha1.ManagedServiceAccount{}).
		Complete(r)
}

func (r *TokenReconciler) ensureServiceAccount(managed *authv1alpha1.ManagedServiceAccount) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.SpokeNamespace,
			Name:      managed.Name,
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

func (r *TokenReconciler) shouldCreateToken(managed *authv1alpha1.ManagedServiceAccount) bool {
	if len(managed.Status.Token) == 0 {
		return true
	}
	now := metav1.Now()
	refreshThreshold := time.Hour * 24 * 15 // 15d
	lifetime := managed.Status.ExpirationTimestamp.Sub(now.Time)
	if lifetime < refreshThreshold {
		return true
	}

	return false
}

func (r *TokenReconciler) createToken(managed *authv1alpha1.ManagedServiceAccount) (string, metav1.Time, error) {
	tr, err := r.SpokeNativeClient.CoreV1().ServiceAccounts(r.SpokeNamespace).
		CreateToken(context.TODO(), managed.Name, &authv1.TokenRequest{}, metav1.CreateOptions{})
	if err != nil {
		return "", metav1.Time{}, err
	}
	return tr.Status.Token, tr.Status.ExpirationTimestamp, nil
}
