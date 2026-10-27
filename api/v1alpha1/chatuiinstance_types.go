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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SupportedImage is the release-pinned Open WebUI image used by managed workloads.
// This multi-platform index matches the image running on cc-d2 on 2026-10-26.
const SupportedImage = "ghcr.io/open-webui/open-webui@sha256:096c8f3d3132016210ca87b0b4dff01d2d66d9fe4fee088fae13fc30f9ac54f3"

// CredentialsSecretReference preserves the required, non-empty name in the CRD.
type CredentialsSecretReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ChatUIInstanceSpec defines the desired state of ChatUIInstance
type ChatUIInstanceSpec struct {
	// CredentialsSecretRef references a Secret containing OPENAI_API_URL and OPENAI_API_KEY.
	// The Secret must be in the same namespace.
	CredentialsSecretRef CredentialsSecretReference `json:"credentialsSecretRef"`

	// Replicas configures the number of UI pods. Defaults to 1.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Image may be omitted to use the supported release-pinned image.
	// An explicit image must exactly match SupportedImage; custom images and tags are rejected.
	// +kubebuilder:validation:Enum="";"ghcr.io/open-webui/open-webui@sha256:096c8f3d3132016210ca87b0b4dff01d2d66d9fe4fee088fae13fc30f9ac54f3"
	Image string `json:"image,omitempty"`
}

// ChatUIInstanceStatus defines the observed state of ChatUIInstance
type ChatUIInstanceStatus struct {
	// AccessSecretUIDs pins server-assigned identities of controller-created keys
	// and the current immutable access bundle. Recreated Secrets are not adopted.
	AccessSecretUIDs map[string]string `json:"accessSecretUIDs,omitempty"`

	// Phase is a high-level summary of the instance lifecycle (e.g., "Ready").
	Phase string `json:"phase,omitempty"`

	// URL is the public URL of the Chat UI.
	URL string `json:"url,omitempty"`

	// Slug is selected by the operator from immutable identity or reviewed configuration.
	// It is reported for consumers and is never an input to hostname selection.
	Slug string `json:"slug,omitempty"`

	// SlugSource identifies the operator's selection method.
	// +kubebuilder:validation:Enum=Derived;Legacy
	SlugSource string `json:"slugSource,omitempty"`

	// ObservedGeneration reflects the generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=cui
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
