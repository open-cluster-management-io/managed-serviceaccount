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
// Code generated by informer-gen. DO NOT EDIT.

package v1beta1

import (
	"context"
	time "time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
	authenticationv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	versioned "open-cluster-management.io/managed-serviceaccount/pkg/generated/clientset/versioned"
	internalinterfaces "open-cluster-management.io/managed-serviceaccount/pkg/generated/informers/externalversions/internalinterfaces"
	v1beta1 "open-cluster-management.io/managed-serviceaccount/pkg/generated/listers/authentication/v1beta1"
)

// ManagedServiceAccountInformer provides access to a shared informer and lister for
// ManagedServiceAccounts.
type ManagedServiceAccountInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1beta1.ManagedServiceAccountLister
}

type managedServiceAccountInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewManagedServiceAccountInformer constructs a new informer for ManagedServiceAccount type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewManagedServiceAccountInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredManagedServiceAccountInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredManagedServiceAccountInformer constructs a new informer for ManagedServiceAccount type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredManagedServiceAccountInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.AuthenticationV1beta1().ManagedServiceAccounts(namespace).List(context.TODO(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.AuthenticationV1beta1().ManagedServiceAccounts(namespace).Watch(context.TODO(), options)
			},
		},
		&authenticationv1beta1.ManagedServiceAccount{},
		resyncPeriod,
		indexers,
	)
}

func (f *managedServiceAccountInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredManagedServiceAccountInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *managedServiceAccountInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&authenticationv1beta1.ManagedServiceAccount{}, f.defaultInformer)
}

func (f *managedServiceAccountInformer) Lister() v1beta1.ManagedServiceAccountLister {
	return v1beta1.NewManagedServiceAccountLister(f.Informer().GetIndexer())
}
