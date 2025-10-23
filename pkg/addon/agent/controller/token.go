package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/client-go/discovery"
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
	HubClient            client.Client
	HubNativeClient      kubernetes.Interface
	SpokeNativeClient    kubernetes.Interface
	SpokeDiscoveryClient discovery.DiscoveryInterface
	SpokeClientConfig    *rest.Config
	SpokeNamespace       string
	ClusterName          string
	SpokeCache           cache.Cache
}

// SetupWithManager sets up the controller with the Manager.
func (r *TokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authv1beta1.ManagedServiceAccount{}).
		Named("managed_serviceaccount_agent_token_controller").
		Watches(
			&corev1.Secret{},
			event.NewSecretEventHandler(),
		).
		WatchesRawSource(
			source.Kind(
				r.SpokeCache,
				&corev1.ServiceAccount{},
				event.NewServiceAccountEventHandler[*corev1.ServiceAccount](r.ClusterName),
			),
		).
		Complete(r)
}

func (r *TokenReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Start reconciling")
	msa := &authv1beta1.ManagedServiceAccount{}

	if err := r.Cache.Get(ctx, request.NamespacedName, msa); err != nil {
		if !apierrors.IsNotFound(err) {
			// fail to get managed-serviceaccount, requeue
			return reconcile.Result{}, errors.Wrapf(err, "fail to get managed serviceaccount")
		}

		saclient := r.SpokeNativeClient.CoreV1().ServiceAccounts(r.SpokeNamespace)
		sa, err := saclient.Get(ctx, request.Name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				// fail to get related serviceaccount, requeue
				return reconcile.Result{}, errors.Wrapf(err, "fail to get related serviceaccount")
			}

			logger.Info("Both ManagedServiceAccount and related ServiceAccount does not exist")
			return reconcile.Result{}, nil
		}

		// check if the serviceacount is managed by the agent, if not, return
		if sa.Labels[common.LabelKeyIsManagedServiceAccount] != "true" {

			logger.Info("Related ServiceAccount is not managed by the agent, skip deletion")
			return reconcile.Result{}, nil
		}

		if err := saclient.Delete(ctx, request.Name, metav1.DeleteOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				// fail to delete related serviceaccount, requeue
				return reconcile.Result{}, errors.Wrapf(err, "fail to delete related serviceaccount")
			}
		}

		logger.Info("Delete related ServiceAccount successfully")
		return reconcile.Result{}, nil
	}

	msaCopy := msa.DeepCopy()
	if err := r.ensureServiceAccount(msaCopy); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to ensure service account")
	}

	expiring, err := r.sync(ctx, msaCopy)
	if err != nil {
		meta.SetStatusCondition(&msaCopy.Status.Conditions, metav1.Condition{
			Type:    authv1beta1.ConditionTypeTokenReported,
			Status:  metav1.ConditionFalse,
			Reason:  "TokenReportFailed",
			Message: err.Error(),
		})
		if errUpdate := r.HubClient.Status().Update(context.TODO(), msaCopy); errUpdate != nil {
			return reconcile.Result{}, errors.Wrapf(errUpdate, "failed to update status")
		}
		return reconcile.Result{}, errors.Wrapf(err, "failed to sync token")
	}

	now := metav1.Now()
	var requeueAfter time.Duration = 0
	if expiring == nil {
		// token is not expiried, no need to refresh, just calculate the requeue time
		if msa.Status.TokenSecretRef == nil || msa.Status.ExpirationTimestamp == nil {
			return reconcile.Result{}, errors.New("token secret ref or expiration time is nil but token not refreshed")
		}

		setManagedServiceAccountSuccessStatus(msaCopy, msa.Status.ExpirationTimestamp,
			now, msa.Status.TokenSecretRef.LastRefreshTimestamp)

		// Requeue even if the token is not refreshed, otherwise if the agent restarts
		// at the time that the token is not expried, no chance to trigger the expiration
		// check again
		requeueAfter = checkTokenRefreshAfter(now,
			*msa.Status.ExpirationTimestamp, msa.Status.TokenSecretRef.LastRefreshTimestamp)

	} else {
		// after sync func succeeds, the secret must exist, add the conditions if not exist
		setManagedServiceAccountSuccessStatus(msaCopy, expiring, now, now)
	}

	if !reflect.DeepEqual(msa.Status, msaCopy.Status) {
		if err := r.HubClient.Status().Update(context.TODO(), msaCopy); err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "failed to update status")
		}
	}

	return reconcile.Result{RequeueAfter: requeueAfter}, nil
}

func setManagedServiceAccountSuccessStatus(msaCopy *authv1beta1.ManagedServiceAccount,
	expiring *metav1.Time, lastTransitionTime, lastRreshTimestamp metav1.Time) {

	// after sync func succeeds, the secret must exist, add the conditions if not exist
	meta.SetStatusCondition(&msaCopy.Status.Conditions, metav1.Condition{
		Type:               authv1beta1.ConditionTypeSecretCreated,
		Status:             metav1.ConditionTrue,
		Reason:             "SecretCreated",
		LastTransitionTime: lastTransitionTime,
	})

	meta.SetStatusCondition(&msaCopy.Status.Conditions, metav1.Condition{
		Type:               authv1beta1.ConditionTypeTokenReported,
		Status:             metav1.ConditionTrue,
		Reason:             "TokenReported",
		LastTransitionTime: lastTransitionTime,
	})

	msaCopy.Status.ExpirationTimestamp = expiring
	msaCopy.Status.TokenSecretRef = &authv1beta1.SecretRef{
		Name:                 msaCopy.Name,
		LastRefreshTimestamp: lastRreshTimestamp,
	}
}

func checkTokenRefreshAfter(now metav1.Time, expiring metav1.Time, lastRefreshTimestamp metav1.Time) time.Duration {
	exceed, threshold := exceedThreshold(now, expiring, lastRefreshTimestamp)
	if exceed {
		return time.Duration(5 * time.Second)
	}
	return threshold.Sub(now.Time) + time.Duration(5*time.Second)
}

// sync is the main logic of token rotation, it returns the expiration time of the token if the token is created/updated
func (r *TokenReconciler) sync(ctx context.Context,
	managed *authv1beta1.ManagedServiceAccount) (*metav1.Time, error) {
	logger := log.FromContext(ctx)
	secretExists := true
	currentTokenSecret := &corev1.Secret{}
	if err := r.HubClient.Get(ctx, types.NamespacedName{
		Namespace: managed.Namespace,
		Name:      managed.Name,
	}, currentTokenSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, errors.Wrapf(err, "failed to read current token secret from hub cluster")
		}
		secretExists = false
		currentTokenSecret = nil
	}

	if shouldCreateUpdate, err := r.shouldCreateUpdateTokenSecret(managed, currentTokenSecret); err != nil {
		return nil, errors.Wrapf(err, "failed to make a decision on token creation")
	} else if secretExists && !shouldCreateUpdate {
		return nil, nil
	}

	token, expiring, err := r.createToken(managed)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to request token for service-account")
	}

	caData := r.SpokeClientConfig.CAData
	if len(caData) == 0 {
		var err error
		caData, err = os.ReadFile(r.SpokeClientConfig.CAFile)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read CA data from file")
		}
	}

	tokenSecret := r.buildSecret(managed, currentTokenSecret, caData, []byte(token))
	if secretExists {
		if err := r.HubClient.Update(ctx, tokenSecret); err != nil {
			return nil, errors.Wrapf(err, "failed to update the token secret")
		}
	} else {
		if err := r.HubClient.Create(ctx, tokenSecret); err != nil {
			return nil, errors.Wrapf(err, "failed to create the token secret")
		}
	}

	logger.Info("Token refreshed", "expirationTimestamp", expiring)
	return &expiring, nil
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

func (r *TokenReconciler) createToken(
	managed *authv1beta1.ManagedServiceAccount) (string, metav1.Time, error) {
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

func (r *TokenReconciler) buildSecret(managed *authv1beta1.ManagedServiceAccount, currentSecret *corev1.Secret,
	caData, tokenData []byte) *corev1.Secret {
	var copySecret *corev1.Secret
	if currentSecret != nil {
		copySecret = currentSecret.DeepCopy()
	} else {
		copySecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: managed.Namespace,
				Name:      managed.Name,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{},
		}
	}

	if copySecret.Labels == nil {
		copySecret.Labels = map[string]string{}
	}
	copySecret.Labels[common.LabelKeyIsManagedServiceAccount] = "true"

	copySecret.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: authv1beta1.GroupVersion.String(),
			Kind:       "ManagedServiceAccount",
			Name:       managed.Name,
			UID:        managed.UID,
		},
	}

	copySecret.Data = map[string][]byte{
		corev1.ServiceAccountRootCAKey: caData,
		corev1.ServiceAccountTokenKey:  tokenData,
	}
	return copySecret
}

func (r *TokenReconciler) shouldCreateUpdateTokenSecret(msa *authv1beta1.ManagedServiceAccount,
	secret *corev1.Secret) (bool, error) {
	if msa.Status.TokenSecretRef == nil || msa.Status.ExpirationTimestamp == nil || secret == nil {
		return true, nil
	}

	token := secret.Data[corev1.ServiceAccountTokenKey]
	if match, err := CheckUserInToken(r.SpokeNamespace, msa.Name, string(token)); err != nil {
		return true, err
	} else if !match {
		return true, nil
	}

	return r.isSoonExpiring(msa, secret)
}

func (r *TokenReconciler) isSoonExpiring(msa *authv1beta1.ManagedServiceAccount, secret *corev1.Secret) (bool, error) {
	if msa.Status.TokenSecretRef == nil || msa.Status.ExpirationTimestamp == nil || secret == nil {
		return true, nil
	}

	now := metav1.Now()
	if exceed, _ := exceedThreshold(now, *msa.Status.ExpirationTimestamp,
		msa.Status.TokenSecretRef.LastRefreshTimestamp); exceed {
		return true, nil
	}

	// check if the token is valid or not
	tokenReview := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{
			Token: string(secret.Data[corev1.ServiceAccountTokenKey]),
		},
	}
	tr, err := r.SpokeNativeClient.AuthenticationV1().TokenReviews().Create(
		context.TODO(), tokenReview, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}

	return !tr.Status.Authenticated, nil
}

func exceedThreshold(now metav1.Time, expiring metav1.Time, lastRefreshTimestamp metav1.Time) (bool, time.Time) {
	// Check if the token should be refreshed, the token will not be rotated unless its remaining lifetime is
	// less than 20% of its rotation validity
	// Some kubernetes distribution may have a maximum token lifetime, for example, eks will shorten the token lifetime
	// to 1 day, so here we use the real expiration time and last refresh time, instead of the requested expiration time
	// in the managedserviceaccount.spec.rotation.validity, to calculate the refresh threshold
	threshold := lastRefreshTimestamp.Add(expiring.Sub(lastRefreshTimestamp.Time) / 5 * 4)
	return now.Time.After(threshold), threshold
}

// CheckUserInToken checks the namespace and name from the `sub` claim in JWT token payload
func CheckUserInToken(namespace, name, token string) (bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false, errors.New("invalid JWT token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, fmt.Errorf("failed to decode payload: %w", err)
	}

	// the payload example :
	// {"aud":["https://kubernetes.default.svc"],"exp":1748219780,"iat":1747614980,
	// "iss":"https://kubernetes.default.svc","jti":"ed5ed5a8-02bc-48eb-8d17-6cbdf56d5e16",
	// "kubernetes.io":{"namespace":"open-cluster-management-agent-addon",
	// "serviceaccount":{"name":"klusterlet-addon-workmgr-log","uid":"ea225e46-7cf3-4939-8cc7-bff0ba8630ad"}},
	// "nbf":1747614980,"sub":"system:serviceaccount:open-cluster-management-agent-addon:klusterlet-addon-workmgr-log"}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false, fmt.Errorf("failed to unmarshal claims: %w", err)
	}

	if sub, ok := claims["sub"].(string); ok {
		return serviceaccount.MatchesUsername(namespace, name, sub), nil
	}

	return false, errors.New("sub claim not found in token claims")
}
