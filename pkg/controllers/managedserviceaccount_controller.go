/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/runtime/inject"

	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
)

var _ inject.Cache = &ManagedServiceAccountReconciler{}
var _ inject.Config = &ManagedServiceAccountReconciler{}

// ManagedServiceAccountReconciler reconciles a ManagedServiceAccount object
type ManagedServiceAccountReconciler struct {
	client.Client
	cache.Cache
	clientConfig *rest.Config
	Scheme       *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedServiceAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authv1alpha1.ManagedServiceAccount{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=authentication.open-cluster-management.io.open-cluster-management.io,resources=managedserviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=authentication.open-cluster-management.io.open-cluster-management.io,resources=managedserviceaccounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=authentication.open-cluster-management.io.open-cluster-management.io,resources=managedserviceaccounts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ManagedServiceAccount object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
func (r *ManagedServiceAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	managed := &authv1alpha1.ManagedServiceAccount{}
	if err := r.Cache.Get(ctx, req.NamespacedName, managed); err != nil {
		return ctrl.Result{}, nil
	}

	switch managed.Spec.Projected.Type {
	case authv1alpha1.ProjectionTypeNone:
		// no op
	case authv1alpha1.ProjectionTypeSecret:
		if err := r.projectSecret(managed); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed projecting secret")
		}
	}

	return ctrl.Result{}, nil
}

func (r *ManagedServiceAccountReconciler) InjectCache(cache cache.Cache) error {
	r.Cache = cache
	return nil
}

func (r *ManagedServiceAccountReconciler) InjectConfig(config *rest.Config) error {
	r.clientConfig = config
	return nil
}

func (r *ManagedServiceAccountReconciler) projectSecret(managed *authv1alpha1.ManagedServiceAccount) error {
	if len(managed.Status.Token) == 0 {
		return nil
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: managed.Spec.Projected.Secret.Namespace,
			Name:      managed.Spec.Projected.Secret.Name,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{},
	}

	namespacedName := types.NamespacedName{
		Namespace: managed.Spec.Projected.Secret.Namespace,
		Name:      managed.Spec.Projected.Secret.Name,
	}

	exists := true
	if err := r.Client.Get(context.TODO(), namespacedName, secret); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		exists = false
	}

	if secret.Labels == nil {
		secret.Labels = make(map[string]string)
	}
	for k, v := range managed.Spec.Projected.Secret.Labels {
		secret.Labels[k] = v
	}
	secret.Data[corev1.ServiceAccountRootCAKey] = managed.Status.CACertificateData
	secret.Data[corev1.ServiceAccountTokenKey] = []byte(managed.Status.Token)

	if !exists {
		return r.Client.Create(context.TODO(), secret)
	}
	return r.Client.Update(context.TODO(), secret)
}
