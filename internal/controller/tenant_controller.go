/*
Copyright 2026 Herman Hoffman.

Licensed under the MIT License. See LICENSE in the project root.
*/

// Package controller contains the Tenant reconciler.
package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	networkingv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
	rbacv1ac "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	tenancyv1alpha1 "github.com/HOFFMAN-ALTERNATIVE/k8s-operator/api/v1alpha1"
)

// Names and labels the operator uses on managed objects.
const (
	Finalizer      = "tenancy.hoffman-alternative.io/finalizer"
	FieldOwner     = "tenant-operator"
	LabelTenant    = "tenancy.hoffman-alternative.io/tenant"
	LabelManagedBy = "app.kubernetes.io/managed-by"

	QuotaName        = "tenant-quota"
	LimitRangeName   = "tenant-defaults"
	DenyPolicyName   = "default-deny-ingress"
	AllowPolicyName  = "allow-same-namespace"
	AdminBindingName = "tenant-admins"
)

// Condition reasons.
const (
	ReasonProvisioned          = "Provisioned"
	ReasonNamespaceConflict    = "NamespaceConflict"
	ReasonNamespaceTerminating = "NamespaceTerminating"
	ReasonApplyFailed          = "ApplyFailed"
	ReasonDeleting             = "Deleting"
)

// TenantReconciler provisions a namespace and its guard rails per Tenant.
type TenantReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// TerminatingRequeue is how often to re-check a terminating namespace.
	TerminatingRequeue time.Duration
}

// +kubebuilder:rbac:groups=tenancy.hoffman-alternative.io,resources=tenants,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=tenancy.hoffman-alternative.io,resources=tenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tenancy.hoffman-alternative.io,resources=tenants/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces;resourcequotas;limitranges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=admin
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile drives one Tenant towards its spec. It is level-triggered and
// idempotent: every call recomputes the full desired state and applies it
// with server-side apply, so drift (someone editing the quota by hand) is
// corrected on the next event for any owned object.
func (r *TenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var t tenancyv1alpha1.Tenant
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !t.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &t)
	}
	if !controllerutil.ContainsFinalizer(&t, Finalizer) {
		patch := client.MergeFrom(t.DeepCopy())
		controllerutil.AddFinalizer(&t, Finalizer)
		if err := r.Patch(ctx, &t, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	nsName := t.TargetNamespace()
	var ns corev1.Namespace
	err := r.Get(ctx, client.ObjectKey{Name: nsName}, &ns)
	switch {
	case err == nil && ns.Labels[LabelTenant] != t.Name:
		// Never adopt a namespace someone else created: that would put
		// another team's workloads under this tenant's quota and admins.
		msg := fmt.Sprintf("namespace %q exists and is not owned by this tenant", nsName)
		r.event(&t, corev1.EventTypeWarning, ReasonNamespaceConflict, msg)
		return ctrl.Result{RequeueAfter: time.Minute}, r.setReady(ctx, &t, metav1.ConditionFalse, ReasonNamespaceConflict, msg, nil)
	case err == nil && !ns.DeletionTimestamp.IsZero():
		msg := fmt.Sprintf("namespace %q is still terminating", nsName)
		return ctrl.Result{RequeueAfter: r.requeue()}, r.setReady(ctx, &t, metav1.ConditionFalse, ReasonNamespaceTerminating, msg, nil)
	case err != nil && !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	managed, err := r.applyAll(ctx, &t, nsName)
	if err != nil {
		logger.Error(err, "apply failed")
		r.event(&t, corev1.EventTypeWarning, ReasonApplyFailed, err.Error())
		if serr := r.setReady(ctx, &t, metav1.ConditionFalse, ReasonApplyFailed, err.Error(), managed); serr != nil {
			return ctrl.Result{}, errors.Join(err, serr)
		}
		return ctrl.Result{}, err
	}
	wasReady := meta.IsStatusConditionTrue(t.Status.Conditions, tenancyv1alpha1.ConditionReady)
	if err := r.setReady(ctx, &t, metav1.ConditionTrue, ReasonProvisioned,
		fmt.Sprintf("namespace %q and %d guard-rail objects are in sync", nsName, len(managed)-1), managed); err != nil {
		return ctrl.Result{}, err
	}
	if !wasReady {
		r.event(&t, corev1.EventTypeNormal, ReasonProvisioned, "tenant namespace "+nsName+" provisioned")
	}
	return ctrl.Result{}, nil
}

// applyAll server-side-applies every desired object and deletes the ones the
// spec no longer asks for. It returns kind/name of what is managed.
func (r *TenantReconciler) applyAll(ctx context.Context, t *tenancyv1alpha1.Tenant, ns string) ([]string, error) {
	owner := metav1ac.OwnerReference().
		WithAPIVersion(tenancyv1alpha1.GroupVersion.String()).WithKind("Tenant").
		WithName(t.Name).WithUID(t.UID).WithController(true).WithBlockOwnerDeletion(true)
	labels := map[string]string{LabelTenant: t.Name, LabelManagedBy: FieldOwner}

	level := string(t.Spec.PodSecurity)
	if level == "" {
		level = "restricted"
	}
	nsLabels := map[string]string{
		"pod-security.kubernetes.io/enforce": level,
		"pod-security.kubernetes.io/audit":   level,
		"pod-security.kubernetes.io/warn":    level,
	}
	for k, v := range labels {
		nsLabels[k] = v
	}
	if err := r.apply(ctx, corev1ac.Namespace(ns).WithLabels(nsLabels).WithOwnerReferences(owner)); err != nil {
		return nil, fmt.Errorf("namespace: %w", err)
	}
	managed := []string{"Namespace/" + ns}

	// ResourceQuota
	if len(t.Spec.Quota) > 0 {
		hard := t.Spec.Quota.DeepCopy()
		if err := r.apply(ctx, corev1ac.ResourceQuota(QuotaName, ns).WithLabels(labels).WithOwnerReferences(owner).
			WithSpec(corev1ac.ResourceQuotaSpec().WithHard(hard))); err != nil {
			return managed, fmt.Errorf("resourcequota: %w", err)
		}
		managed = append(managed, "ResourceQuota/"+QuotaName)
	} else if err := r.prune(ctx, &corev1.ResourceQuota{}, QuotaName, ns); err != nil {
		return managed, err
	}

	// LimitRange
	if d := t.Spec.DefaultLimits; d != nil && (len(d.Requests) > 0 || len(d.Limits) > 0) {
		item := corev1ac.LimitRangeItem().WithType(corev1.LimitTypeContainer)
		if len(d.Requests) > 0 {
			item = item.WithDefaultRequest(d.Requests.DeepCopy())
		}
		if len(d.Limits) > 0 {
			item = item.WithDefault(d.Limits.DeepCopy())
		}
		if err := r.apply(ctx, corev1ac.LimitRange(LimitRangeName, ns).WithLabels(labels).WithOwnerReferences(owner).
			WithSpec(corev1ac.LimitRangeSpec().WithLimits(item))); err != nil {
			return managed, fmt.Errorf("limitrange: %w", err)
		}
		managed = append(managed, "LimitRange/"+LimitRangeName)
	} else if err := r.prune(ctx, &corev1.LimitRange{}, LimitRangeName, ns); err != nil {
		return managed, err
	}

	// NetworkPolicies: deny all ingress, then allow traffic from pods in
	// the same namespace. Egress stays open (DNS etc.) — tighten per tenant.
	if t.IsolationEnabled() {
		deny := networkingv1ac.NetworkPolicy(DenyPolicyName, ns).WithLabels(labels).WithOwnerReferences(owner).
			WithSpec(networkingv1ac.NetworkPolicySpec().
				WithPodSelector(metav1ac.LabelSelector()).
				WithPolicyTypes(networkingv1.PolicyTypeIngress))
		allow := networkingv1ac.NetworkPolicy(AllowPolicyName, ns).WithLabels(labels).WithOwnerReferences(owner).
			WithSpec(networkingv1ac.NetworkPolicySpec().
				WithPodSelector(metav1ac.LabelSelector()).
				WithPolicyTypes(networkingv1.PolicyTypeIngress).
				WithIngress(networkingv1ac.NetworkPolicyIngressRule().
					WithFrom(networkingv1ac.NetworkPolicyPeer().WithPodSelector(metav1ac.LabelSelector()))))
		for _, p := range []*networkingv1ac.NetworkPolicyApplyConfiguration{deny, allow} {
			if err := r.apply(ctx, p); err != nil {
				return managed, fmt.Errorf("networkpolicy: %w", err)
			}
		}
		managed = append(managed, "NetworkPolicy/"+DenyPolicyName, "NetworkPolicy/"+AllowPolicyName)
	} else {
		for _, name := range []string{DenyPolicyName, AllowPolicyName} {
			if err := r.prune(ctx, &networkingv1.NetworkPolicy{}, name, ns); err != nil {
				return managed, err
			}
		}
	}

	// Admin RoleBinding to the built-in "admin" ClusterRole.
	if len(t.Spec.Admins) > 0 {
		subjects := make([]*rbacv1ac.SubjectApplyConfiguration, 0, len(t.Spec.Admins))
		for _, s := range t.Spec.Admins {
			sac := rbacv1ac.Subject().WithKind(s.Kind).WithName(s.Name)
			if s.APIGroup != "" {
				sac = sac.WithAPIGroup(s.APIGroup)
			} else if s.Kind == rbacv1.UserKind || s.Kind == rbacv1.GroupKind {
				sac = sac.WithAPIGroup(rbacv1.GroupName)
			}
			if s.Namespace != "" {
				sac = sac.WithNamespace(s.Namespace)
			}
			subjects = append(subjects, sac)
		}
		if err := r.apply(ctx, rbacv1ac.RoleBinding(AdminBindingName, ns).WithLabels(labels).WithOwnerReferences(owner).
			WithRoleRef(rbacv1ac.RoleRef().WithAPIGroup(rbacv1.GroupName).WithKind("ClusterRole").WithName("admin")).
			WithSubjects(subjects...)); err != nil {
			return managed, fmt.Errorf("rolebinding: %w", err)
		}
		managed = append(managed, "RoleBinding/"+AdminBindingName)
	} else if err := r.prune(ctx, &rbacv1.RoleBinding{}, AdminBindingName, ns); err != nil {
		return managed, err
	}
	return managed, nil
}

func (r *TenantReconciler) apply(ctx context.Context, obj runtime.ApplyConfiguration) error {
	return r.Apply(ctx, obj, client.FieldOwner(FieldOwner), client.ForceOwnership)
}

// prune deletes a managed object the spec no longer wants. Objects without
// our tenant label are left alone.
func (r *TenantReconciler) prune(ctx context.Context, obj client.Object, name, ns string) error {
	if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if obj.GetLabels()[LabelManagedBy] != FieldOwner {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, obj))
}

// finalize implements the deletion policy, then releases the finalizer.
//
//   - Delete: delete the namespace and keep the Tenant (and its finalizer)
//     until the namespace is really gone, so re-creating a Tenant with the
//     same name can't race a terminating namespace.
//   - Retain: drop our owner reference and labels from the namespace so the
//     garbage collector leaves it alone, then let the Tenant go.
func (r *TenantReconciler) finalize(ctx context.Context, t *tenancyv1alpha1.Tenant) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(t, Finalizer) {
		return ctrl.Result{}, nil
	}
	nsName := t.TargetNamespace()
	var ns corev1.Namespace
	err := r.Get(ctx, client.ObjectKey{Name: nsName}, &ns)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	owned := err == nil && ns.Labels[LabelTenant] == t.Name

	if owned && t.Spec.DeletionPolicy == tenancyv1alpha1.DeletionPolicyRetain {
		patch := client.MergeFrom(ns.DeepCopy())
		ns.OwnerReferences = slices.DeleteFunc(ns.OwnerReferences, func(o metav1.OwnerReference) bool { return o.UID == t.UID })
		delete(ns.Labels, LabelTenant)
		delete(ns.Labels, LabelManagedBy)
		if err := r.Patch(ctx, &ns, patch); err != nil {
			return ctrl.Result{}, err
		}
		r.event(t, corev1.EventTypeNormal, "Retained", "namespace "+nsName+" orphaned (deletionPolicy=Retain)")
	} else if owned {
		if ns.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &ns, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			r.event(t, corev1.EventTypeNormal, ReasonDeleting, "deleting namespace "+nsName)
		}
		if err := r.setReady(ctx, t, metav1.ConditionFalse, ReasonDeleting, "waiting for namespace "+nsName+" to terminate", nil); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.requeue()}, nil
	}

	patch := client.MergeFrom(t.DeepCopy())
	controllerutil.RemoveFinalizer(t, Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, t, patch))
}

func (r *TenantReconciler) setReady(ctx context.Context, t *tenancyv1alpha1.Tenant, status metav1.ConditionStatus, reason, msg string, managed []string) error {
	patch := client.MergeFrom(t.DeepCopy())
	t.Status.ObservedGeneration = t.Generation
	t.Status.Namespace = t.TargetNamespace()
	if managed != nil {
		t.Status.ManagedResources = managed
	}
	meta.SetStatusCondition(&t.Status.Conditions, metav1.Condition{
		Type: tenancyv1alpha1.ConditionReady, Status: status, Reason: reason, Message: msg,
		ObservedGeneration: t.Generation,
	})
	return client.IgnoreNotFound(r.Status().Patch(ctx, t, patch))
}

func (r *TenantReconciler) event(t *tenancyv1alpha1.Tenant, typ, reason, note string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(t, nil, typ, reason, "Reconcile", "%s", note)
	}
}

func (r *TenantReconciler) requeue() time.Duration {
	if r.TerminatingRequeue > 0 {
		return r.TerminatingRequeue
	}
	return 5 * time.Second
}

// SetupWithManager wires watches: Tenants (spec changes only — status
// updates don't retrigger), plus every owned kind for drift correction.
func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Setting deletionTimestamp bumps .metadata.generation on custom
		// resources, so deletions pass this predicate too.
		For(&tenancyv1alpha1.Tenant{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&corev1.LimitRange{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.RoleBinding{}).
		Named("tenant").
		Complete(r)
}
