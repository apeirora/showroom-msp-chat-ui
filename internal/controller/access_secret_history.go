package controller

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strings"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Retain enough trusted identities for old CA -> overlap -> new CA -> overlap
// rollback. The session key has its own permanent pin outside this limit.
const maxAccessBundles = 3

// makeAccessSecretRoom retires at most one previously pinned bundle before
// creating a fourth. Never adopt an orphan, evict a live mount, or forget a pin
// before deletion is confirmed. A crash between deletion and the status patch
// is resumable: a missing non-desired, unused bundle can finish retirement.
// The desired bundle is checked for a missing/replaced UID before this is called.
func (r *ChatUIInstanceReconciler) makeAccessSecretRoom(ctx context.Context, inst *uiapi.ChatUIInstance) error {
	if len(inst.Status.AccessSecretUIDs) <= maxAccessBundles {
		return nil
	}
	var deployments appsv1.DeploymentList
	if err := r.routeReader().List(ctx, &deployments, client.InNamespace(inst.Namespace)); err != nil {
		return err
	}
	var pods corev1.PodList
	if err := r.routeReader().List(ctx, &pods, client.InNamespace(inst.Namespace)); err != nil {
		return err
	}
	referenced := make(map[string]bool)
	for i := range deployments.Items {
		collectSecretReferences(&deployments.Items[i].Spec.Template.Spec, referenced)
	}
	for i := range pods.Items {
		// Include terminating Pods: their mounted bundle is still in use.
		collectSecretReferences(&pods.Items[i].Spec, referenced)
	}
	candidates := make([]corev1.Secret, 0, maxAccessBundles)
	for name, uid := range inst.Status.AccessSecretUIDs {
		if name == sessionSecretName(inst) {
			continue
		}
		if !strings.HasPrefix(name, "chatui-access-") || uid == "" {
			return errors.New("invalid access Secret history")
		}
		var secret corev1.Secret
		err := r.routeReader().Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: name}, &secret)
		if apierrors.IsNotFound(err) {
			secret.ObjectMeta = metav1.ObjectMeta{Namespace: inst.Namespace, Name: name}
		} else if err != nil {
			return err
		} else if !ownedByInstance(&secret, inst) || secret.Immutable == nil || !*secret.Immutable || string(secret.UID) != uid {
			return errors.New("access Secret history ownership or identity mismatch")
		}
		if !referenced[name] {
			candidates = append(candidates, secret)
		}
	}
	if len(candidates) == 0 {
		return errors.New("access Secret history is still in use; cannot retire a bundle")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreationTimestamp.Equal(&candidates[j].CreationTimestamp) {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].CreationTimestamp.Before(&candidates[j].CreationTimestamp)
	})
	retired := &candidates[0]
	if retired.UID != "" {
		if err := r.Delete(ctx, retired, client.Preconditions{UID: &retired.UID, ResourceVersion: &retired.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		var remaining corev1.Secret
		if err := r.routeReader().Get(ctx, client.ObjectKeyFromObject(retired), &remaining); err == nil {
			return errors.New("waiting for retired access Secret deletion")
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}
	pins := maps.Clone(inst.Status.AccessSecretUIDs)
	delete(pins, retired.Name)
	return r.persistAccessSecretPins(ctx, inst, pins)
}

func collectSecretReferences(spec *corev1.PodSpec, names map[string]bool) {
	for _, volume := range spec.Volumes {
		if volume.Secret != nil {
			names[volume.Secret.SecretName] = true
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil {
					names[source.Secret.Name] = true
				}
			}
		}
	}
	for _, pull := range spec.ImagePullSecrets {
		names[pull.Name] = true
	}
	containers := append([]corev1.Container{}, spec.InitContainers...)
	containers = append(containers, spec.Containers...)
	for _, ephemeral := range spec.EphemeralContainers {
		containers = append(containers, corev1.Container{Env: ephemeral.Env, EnvFrom: ephemeral.EnvFrom})
	}
	for _, container := range containers {
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				names[env.ValueFrom.SecretKeyRef.Name] = true
			}
		}
		for _, env := range container.EnvFrom {
			if env.SecretRef != nil {
				names[env.SecretRef.Name] = true
			}
		}
	}
}
