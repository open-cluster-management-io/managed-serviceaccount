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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&ManagedServiceAccount{}, &ManagedServiceAccountList{})
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ManagedServiceAccount is the Schema for the managedserviceaccounts API
type ManagedServiceAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedServiceAccountSpec   `json:"spec,omitempty"`
	Status ManagedServiceAccountStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ManagedServiceAccountList contains a list of ManagedServiceAccount
type ManagedServiceAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedServiceAccount `json:"items"`
}

// ManagedServiceAccountSpec defines the desired state of ManagedServiceAccount
type ManagedServiceAccountSpec struct {
	// Rotation is the policy for rotation the credentials.
	Rotation ManagedServiceAccountRotation `json:"rotation"`
}

// ManagedServiceAccountStatus defines the observed state of ManagedServiceAccount
type ManagedServiceAccountStatus struct {
	// Conditions is the condition list.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ExpirationTimestamp is the time when the token will expire.
	// +optional
	ExpirationTimestamp *metav1.Time `json:"expirationTimestamp,omitempty"`
	// TokenSecretRef is a reference to the secret resource where we store
	// the CA certficate and token for the corresponding service account from
	// the managed cluster.
	TokenSecretRef *SecretRef `json:"tokenSecretRef,omitempty"`
}

type ProjectionType string

const (
	ProjectionTypeNone   ProjectionType = "None"
	ProjectionTypeSecret ProjectionType = "Secret"
)

type ManagedServiceAccountRotation struct {
	// Enabled prescribes whether the service account token will
	// be rotated from the upstream
	// +optional
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
	// Validity prescribes how long is the signed service account token valid.
	// +optional
	// +kubebuilder:default="8640h0m0s"
	Validity metav1.Duration `json:"validity"`
}

type SecretRef struct {
	// Name is the name of the referenced secret.
	// +required
	Name string `json:"name"`
	// LastRefreshTimestamp is the timestamp when the token in the secret
	// is refreshed.
	// +required
	LastRefreshTimestamp metav1.Time `json:"lastRefreshTimestamp"`
}

const (
	ConditionTypeSecretCreated string = "SecretCreated"
	ConditionTypeTokenReported string = "TokenReported"
)
