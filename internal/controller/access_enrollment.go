package controller

import (
	"context"
	"errors"
	"strings"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errEnrollmentUnavailable = errors.New("trusted enrollment configuration temporarily unavailable")

// selectAccessEntry leaves reviewed static entries independent of enrollment.
// Only the operator's configured namespace/name is read; instance metadata never
// selects the ConfigMap or an issuer. An uncached read observes revocation on the
// next existing reconciliation cycle without restarting any unrelated workload.
func (r *ChatUIInstanceReconciler) selectAccessEntry(ctx context.Context, inst *uiapi.ChatUIInstance) (accesspolicy.Entry, error) {
	entry, err := r.AccessPolicy.Select(inst)
	if err == nil || r.AccessPolicy == nil || r.AccessPolicy.Enrollment == nil {
		return entry, err
	}
	var config corev1.ConfigMap
	if err := r.routeReader().Get(ctx, client.ObjectKey{
		Namespace: r.AccessPolicy.CredentialsNamespace,
		Name:      r.AccessPolicy.Enrollment.ConfigMapName,
	}, &config); err != nil {
		if !apierrors.IsNotFound(err) {
			return accesspolicy.Entry{}, errEnrollmentUnavailable
		}
		return accesspolicy.Entry{}, errors.New("trusted enrollment configuration unavailable")
	}
	if config.DeletionTimestamp != nil || config.UID == "" ||
		config.Labels["app.kubernetes.io/managed-by"] != "chat-ui-enrollment" {
		return accesspolicy.Entry{}, errors.New("trusted enrollment configuration identity invalid")
	}
	return r.AccessPolicy.SelectEnrolled(inst, strings.NewReader(config.Data[accesspolicy.EnrollmentDataKey]))
}
