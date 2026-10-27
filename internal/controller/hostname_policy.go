package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	uiapi "github.com/apeirora/showroom-msp-chat-ui/api/v1alpha1"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const hostnameRecheckAfter = 30 * time.Second

var (
	errIngressNotOwned  = errors.New("named Ingress is not controlled by this ChatUIInstance")
	errHostnameConflict = errors.New("another Ingress claims this hostname")
)

func (r *ChatUIInstanceReconciler) routeReader() client.Reader {
	if r.RouteReader != nil {
		return r.RouteReader
	}
	return r.Client
}

func ownsIngress(inst *uiapi.ChatUIInstance, ing *networkingv1.Ingress) bool {
	owner := metav1.GetControllerOf(ing)
	return owner != nil && inst.UID != "" && ing.Namespace == inst.Namespace && owner.UID == inst.UID &&
		owner.Name == inst.Name && owner.Kind == "ChatUIInstance" && owner.APIVersion == uiapi.GroupVersion.String()
}

func (r *ChatUIInstanceReconciler) checkHostname(ctx context.Context, inst *uiapi.ChatUIInstance, host string) error {
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-chatui"}
	var existing networkingv1.Ingress
	if err := r.routeReader().Get(ctx, key, &existing); err == nil {
		if !ownsIngress(inst, &existing) {
			return errIngressNotOwned
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	var ingresses networkingv1.IngressList
	if err := r.routeReader().List(ctx, &ingresses); err != nil {
		return err
	}
	for i := range ingresses.Items {
		ing := &ingresses.Items[i]
		if client.ObjectKeyFromObject(ing) == key && ownsIngress(inst, ing) {
			continue
		}
		for _, rule := range ing.Spec.Rules {
			if claimsHost(rule.Host, host) {
				return errHostnameConflict
			}
		}
	}
	// This read detects existing Ingress claims. It is not an atomic reservation
	// against other controllers or coverage of alternate routing CRDs.
	return nil
}

func claimsHost(rule, host string) bool {
	rule, host = strings.ToLower(rule), strings.ToLower(host)
	if rule == host {
		return true
	}
	if strings.HasPrefix(rule, "*.") {
		suffix := rule[2:]
		return strings.HasSuffix(host, "."+suffix) && strings.Count(host, ".") == strings.Count(suffix, ".")+1
	}
	return false
}

func (r *ChatUIInstanceReconciler) hostnameFailure(ctx context.Context, inst *uiapi.ChatUIInstance, cause error) (ctrl.Result, error) {
	var reason, message string
	switch {
	case errors.Is(cause, hostnamepolicy.ErrLegacyUnmapped):
		reason, message = "LegacyHostnameUnmapped", "An operator-reviewed hostname mapping is required for this existing instance"
	case errors.Is(cause, hostnamepolicy.ErrIdentity):
		reason, message = "HostnameIdentityError", "Instance identity does not match the operator hostname policy"
	case errors.Is(cause, errIngressNotOwned):
		reason, message = "HostnameOwnershipError", "The named Ingress is not controlled by this ChatUIInstance"
	case errors.Is(cause, errHostnameConflict):
		reason, message = "HostnameConflict", "Another Ingress claims the selected hostname; operator review is required"
	default:
		return ctrl.Result{}, cause
	}
	before := inst.DeepCopy()
	if errors.Is(cause, hostnamepolicy.ErrLegacyUnmapped) || errors.Is(cause, hostnamepolicy.ErrIdentity) {
		inst.Status.Slug, inst.Status.SlugSource = "", ""
	}
	inst.Status.Phase, inst.Status.URL = phaseError, ""
	inst.Status.ObservedGeneration = inst.Generation
	meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason,
		Message: message, ObservedGeneration: inst.Generation,
	})
	var err error
	if !reflect.DeepEqual(before.Status, inst.Status) {
		err = r.Status().Patch(ctx, inst, client.MergeFrom(before))
	}
	return ctrl.Result{RequeueAfter: hostnameRecheckAfter}, err
}
