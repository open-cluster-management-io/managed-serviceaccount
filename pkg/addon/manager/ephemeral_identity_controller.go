package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
)

func NewEphemeralIdentityReconciler(cache cache.Cache, hubClient client.Client) *EphemeralIdentityReconciler {
	return &EphemeralIdentityReconciler{
		Cache:     cache,
		HubClient: hubClient,
		clock:     clock.RealClock{},
	}
}

var _ reconcile.Reconciler = &EphemeralIdentityReconciler{}

type EphemeralIdentityReconciler struct {
	clock clock.Clock
	cache.Cache
	HubClient client.Client
}

// SetupWithManager sets up the controller with the Manager.
func (r *EphemeralIdentityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authv1alpha1.ManagedServiceAccount{}).
		Watches(
			&source.Kind{
				Type: &authv1alpha1.ManagedServiceAccount{},
			},
			&handler.EnqueueRequestForObject{},
		).Complete(r)
}

func (r *EphemeralIdentityReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Start reconciling")
	managed := &authv1alpha1.ManagedServiceAccount{}
	if err := r.Cache.Get(ctx, request.NamespacedName, managed); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, errors.Wrapf(err, "fail to get managed serviceaccount")
		}
		logger.Info("No such resource")
		return reconcile.Result{}, nil
	}

	if managed.Spec.TTLSecondsAfterCreation == nil {
		//TTLSecondsAfterCreation is not set don't requeue
		return reconcile.Result{}, nil
	}

	currentTime := r.clock.Now()
	deletionTime := managed.CreationTimestamp.Add(
		time.Duration(*managed.Spec.TTLSecondsAfterCreation) * time.Second,
	)

	if currentTime.After(deletionTime) {
		//delete ManagedServiceAccount
		if err := r.HubClient.Delete(context.TODO(), managed); err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "fail to delete expired ManagedServiceAccount")
		}
		return reconcile.Result{}, nil
	}

	requeueAfter := deletionTime.Sub(currentTime)
	if requeueAfter < 0 {
		return reconcile.Result{}, fmt.Errorf("unexpected error, requeue")
	}

	return reconcile.Result{
		Requeue:      true,
		RequeueAfter: deletionTime.Sub(currentTime),
	}, nil
}
