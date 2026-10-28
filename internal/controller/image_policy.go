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

package controller

import (
	"context"
	"errors"
	"fmt"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const phaseError = "Error"

var errDeploymentNotOwned = errors.New("deployment is not owned by this ChatUIInstance")

func resolveImage(requested string) (string, error) {
	if requested == "" || requested == uiapi.SupportedImage {
		return uiapi.SupportedImage, nil
	}
	return "", fmt.Errorf("spec.image must be omitted or equal the supported image %s", uiapi.SupportedImage)
}

func (r *ChatUIInstanceReconciler) rejectImage(ctx context.Context, inst *uiapi.ChatUIInstance, message string) error {
	withdrawErr := r.withdrawAccess(ctx, inst)
	_, stopErr := r.stopUnapprovedDeployment(ctx, inst, true)
	statusErr := r.updateImagePolicyStatus(ctx, inst, phaseError, "ImageNotAllowed", message)
	return errors.Join(withdrawErr, stopErr, statusErr)
}

func (r *ChatUIInstanceReconciler) updateImagePolicyStatus(ctx context.Context, inst *uiapi.ChatUIInstance, phase, reason, message string) error {
	before := inst.DeepCopy()
	inst.Status.URL = ""
	inst.Status.Phase = phase
	inst.Status.ObservedGeneration = inst.Generation
	meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason,
		Message: message, ObservedGeneration: inst.Generation,
	})
	return r.Status().Patch(ctx, inst, client.MergeFrom(before))
}

func (r *ChatUIInstanceReconciler) stopUnapprovedDeployment(ctx context.Context, inst *uiapi.ChatUIInstance, force bool) (bool, error) {
	// Stored objects can predate admission validation. Stop an existing owned
	// workload too: merely rejecting future updates would leave its image running.
	var deployment appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	if err := r.routeReader().Get(ctx, key, &deployment); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if !ownedByInstance(&deployment, inst) {
		return false, fmt.Errorf("%w: %s", errDeploymentNotOwned, key)
	}
	unapproved := force
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "open-webui" && container.Image != uiapi.SupportedImage {
			unapproved = true
		}
	}
	if !unapproved {
		return false, nil
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
		deployment.Spec.Replicas = ptrTo(int32(0))
		return true, r.Update(ctx, &deployment)
	}
	// Do not immediately scale back up: the Deployment controller must first
	// observe the scale-down, including when credentials are missing during migration.
	// Terminating pods can outlive status.replicas; rollout acceptance must check them.
	return deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.Replicas != 0, nil
}
