/*
Copyright 2026.

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

// HomelabAppSpec defines the desired state of HomelabApp
type HomelabAppSpec struct {
	// Repo is the Git repo URL containing the app manifests
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`

	//Path is the directory within the repo containing the app manifests
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Domain is the public hostname to expose the app on
	// +optional
	Domain string `json:"domain,omitempty"`

	//TargetNamespace is the namespace the app will be deployed into
	// +kubebuilder:validation:Required
	TargetNamespace string `json:"targetNamespace"`

	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html
}

// HomelabAppStatus defines the observed state of HomelabApp.
type HomelabAppStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the HomelabApp resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type="string",JSONPath=".spec.domain"
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".spec.targetNamespace"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"

// HomelabApp is the Schema for the homelabapps API
type HomelabApp struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of HomelabApp
	// +required
	Spec HomelabAppSpec `json:"spec"`

	// status defines the observed state of HomelabApp
	// +optional
	Status HomelabAppStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HomelabAppList contains a list of HomelabApp
type HomelabAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []HomelabApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomelabApp{}, &HomelabAppList{})
}
