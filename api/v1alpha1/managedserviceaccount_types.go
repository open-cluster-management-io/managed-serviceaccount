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

// ManagedServiceAccountSpec defines the desired state of ManagedServiceAccount
type ManagedServiceAccountSpec struct {
	// Projected prescribes how to persist the credentials from the upstream
	// service-account.
	Projected ManagedServiceAccountProjected `json:"projected"`
	// Rotation is the policy for rotation the credentials.
	Rotation ManagedServiceAccountRotation `json:"rotation"`
}

// ManagedServiceAccountStatus defines the observed state of ManagedServiceAccount
type ManagedServiceAccountStatus struct {
	// Conditions is the condition list.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Token is the content of the service account token
	// +optional
	Token string `json:"token,omitempty"`
	// ExpirationTimestamp is the time when the token will expire.
	// +optional
	ExpirationTimestamp *metav1.Time `json:"expirationTimestamp"`
	// CACertificateData is the ca certificate of the managed cluster.
	CACertificateData []byte `json:"caCertificateData,omitempty"`
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

type ManagedServiceAccountProjected struct {
	// Type is the union discriminator for projection type.
	// +optional
	// +kubebuilder:default=None
	Type ProjectionType `json:"type"`
	// Secret prescribes how to project the upstream service account into
	// secrets in the hub cluster.
	// +optional
	Secret *ProjectedSecret `json:"secret,omitempty"`
}

type ProjectionType string

const (
	ProjectionTypeNone   ProjectionType = "None"
	ProjectionTypeSecret ProjectionType = "Secret"
)

type ProjectedSecret struct {
	// Namespace is the namespace of the projected secret
	Namespace string `json:"namespace"`
	// Name is the name of the projected secret
	Name string `json:"name"`
	// Labels is the additional labels attaching to the projected secret.
	Labels map[string]string `json:"labels"`
}

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

//+kubebuilder:object:root=true

// ManagedServiceAccountList contains a list of ManagedServiceAccount
type ManagedServiceAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedServiceAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ManagedServiceAccount{}, &ManagedServiceAccountList{})
}
