/*
Copyright 2025.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChatUIInstanceSpec defines the desired state of ChatUIInstance
type ChatUIInstanceSpec struct {
	// CredentialsSecretRef references a Secret containing OPENAI_API_URL and OPENAI_API_KEY.
	// The Secret must be in the same namespace.
	CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`

	// Replicas configures the number of UI pods. Defaults to 1.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Image optionally overrides the UI container image.
	// e.g., ghcr.io/open-webui/open-webui:latest
	Image string `json:"image,omitempty"`
}

// ChatUIInstanceStatus defines the observed state of ChatUIInstance
type ChatUIInstanceStatus struct {
	// Phase is a high-level summary of the instance lifecycle (e.g., "Ready").
	Phase string `json:"phase,omitempty"`

	// URL is the public URL of the Chat UI.
	URL string `json:"url,omitempty"`

	// ObservedGeneration reflects the generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.spec.credentialsSecretRef.name`
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`

// ChatUIInstance is the Schema for the chatuiinstances API
type ChatUIInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChatUIInstanceSpec   `json:"spec,omitempty"`
	Status ChatUIInstanceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ChatUIInstanceList contains a list of ChatUIInstance
type ChatUIInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChatUIInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChatUIInstance{}, &ChatUIInstanceList{})
}
