/*
Copyright 2026 Herman Hoffman.

Licensed under the MIT License. See LICENSE in the project root.
*/

package controller

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tenancyv1alpha1 "github.com/HOFFMAN-ALTERNATIVE/k8s-operator/api/v1alpha1"
)

func newTenant(name string, mut ...func(*tenancyv1alpha1.Tenant)) *tenancyv1alpha1.Tenant {
	t := &tenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.TenantSpec{
			Quota: corev1.ResourceList{
				corev1.ResourceRequestsCPU:    resource.MustParse("4"),
				corev1.ResourceRequestsMemory: resource.MustParse("8Gi"),
				corev1.ResourcePods:           resource.MustParse("50"),
			},
			DefaultLimits: &tenancyv1alpha1.ContainerDefaults{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			Admins: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: name + "-admins"}},
		},
	}
	for _, m := range mut {
		m(t)
	}
	return t
}

func create(t *testing.T, obj client.Object) {
	t.Helper()
	if err := k8s.Create(testCtx, obj); err != nil {
		t.Fatalf("create %s: %v", obj.GetName(), err)
	}
}

func getTenant(t *testing.T, name string) *tenancyv1alpha1.Tenant {
	t.Helper()
	var tn tenancyv1alpha1.Tenant
	if err := k8s.Get(testCtx, client.ObjectKey{Name: name}, &tn); err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	return &tn
}

func readyReason(name string) (metav1.ConditionStatus, string, error) {
	var tn tenancyv1alpha1.Tenant
	if err := k8s.Get(testCtx, client.ObjectKey{Name: name}, &tn); err != nil {
		return "", "", err
	}
	c := meta.FindStatusCondition(tn.Status.Conditions, tenancyv1alpha1.ConditionReady)
	if c == nil {
		return "", "", errors.New("no Ready condition yet")
	}
	if c.ObservedGeneration != tn.Generation {
		return "", "", fmt.Errorf("condition is for generation %d, object is at %d", c.ObservedGeneration, tn.Generation)
	}
	return c.Status, c.Reason, nil
}

func waitReady(t *testing.T, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	eventually(t, func() error {
		s, r, err := readyReason(name)
		if err != nil {
			return err
		}
		if s != status || r != reason {
			return fmt.Errorf("Ready=%s/%s, want %s/%s", s, r, status, reason)
		}
		return nil
	})
}

func exists(obj client.Object, name, ns string) error {
	return k8s.Get(testCtx, client.ObjectKey{Name: name, Namespace: ns}, obj)
}

func gone(obj client.Object, name, ns string) error {
	err := exists(obj, name, ns)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("%T %s/%s still exists", obj, ns, name)
	}
	return err
}

func TestProvisionsNamespaceAndGuardRails(t *testing.T) {
	create(t, newTenant("team-alpha"))
	waitReady(t, "team-alpha", metav1.ConditionTrue, ReasonProvisioned)

	var ns corev1.Namespace
	if err := exists(&ns, "team-alpha", ""); err != nil {
		t.Fatal(err)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || ns.Labels[LabelTenant] != "team-alpha" {
		t.Fatalf("namespace labels: %v", ns.Labels)
	}
	tn := getTenant(t, "team-alpha")
	if len(ns.OwnerReferences) != 1 || ns.OwnerReferences[0].UID != tn.UID || !ptr.Deref(ns.OwnerReferences[0].Controller, false) {
		t.Fatalf("namespace not controlled by tenant: %+v", ns.OwnerReferences)
	}
	if !strings.Contains(strings.Join(tn.Finalizers, ","), Finalizer) {
		t.Fatalf("finalizer missing: %v", tn.Finalizers)
	}

	var q corev1.ResourceQuota
	if err := exists(&q, QuotaName, "team-alpha"); err != nil {
		t.Fatal(err)
	}
	if got := q.Spec.Hard[corev1.ResourcePods]; got.String() != "50" {
		t.Fatalf("quota pods = %s", got.String())
	}
	var lr corev1.LimitRange
	if err := exists(&lr, LimitRangeName, "team-alpha"); err != nil {
		t.Fatal(err)
	}
	if d := lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU]; d.String() != "100m" {
		t.Fatalf("limitrange default cpu request = %s", d.String())
	}
	for _, name := range []string{DenyPolicyName, AllowPolicyName} {
		var np networkingv1.NetworkPolicy
		if err := exists(&np, name, "team-alpha"); err != nil {
			t.Fatal(err)
		}
	}
	var rb rbacv1.RoleBinding
	if err := exists(&rb, AdminBindingName, "team-alpha"); err != nil {
		t.Fatal(err)
	}
	if rb.RoleRef.Name != "admin" || len(rb.Subjects) != 1 || rb.Subjects[0].Name != "team-alpha-admins" || rb.Subjects[0].APIGroup != rbacv1.GroupName {
		t.Fatalf("rolebinding: %+v %+v", rb.RoleRef, rb.Subjects)
	}
	if tn.Status.Namespace != "team-alpha" || len(tn.Status.ManagedResources) != 6 || tn.Status.ObservedGeneration != tn.Generation {
		t.Fatalf("status: %+v", tn.Status)
	}
}

func TestDriftIsCorrected(t *testing.T) {
	create(t, newTenant("team-drift"))
	waitReady(t, "team-drift", metav1.ConditionTrue, ReasonProvisioned)

	// Someone "temporarily" raises their own quota by hand…
	var q corev1.ResourceQuota
	if err := exists(&q, QuotaName, "team-drift"); err != nil {
		t.Fatal(err)
	}
	q.Spec.Hard[corev1.ResourcePods] = resource.MustParse("9999")
	if err := k8s.Update(testCtx, &q); err != nil {
		t.Fatal(err)
	}
	// …and deletes the default-deny policy.
	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: DenyPolicyName, Namespace: "team-drift"}}
	if err := k8s.Delete(testCtx, np); err != nil {
		t.Fatal(err)
	}

	eventually(t, func() error {
		var q corev1.ResourceQuota
		if err := exists(&q, QuotaName, "team-drift"); err != nil {
			return err
		}
		if got := q.Spec.Hard[corev1.ResourcePods]; got.String() != "50" {
			return fmt.Errorf("quota pods still %s", got.String())
		}
		return exists(&networkingv1.NetworkPolicy{}, DenyPolicyName, "team-drift")
	})
}

func TestSpecChangesPruneObjects(t *testing.T) {
	create(t, newTenant("team-prune"))
	waitReady(t, "team-prune", metav1.ConditionTrue, ReasonProvisioned)

	tn := getTenant(t, "team-prune")
	tn.Spec.Quota = nil
	tn.Spec.DefaultLimits = nil
	tn.Spec.Admins = nil
	tn.Spec.NetworkIsolation = ptr.To(false)
	tn.Spec.PodSecurity = "baseline"
	if err := k8s.Update(testCtx, tn); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		for _, c := range []struct {
			obj  client.Object
			name string
		}{
			{&corev1.ResourceQuota{}, QuotaName},
			{&corev1.LimitRange{}, LimitRangeName},
			{&networkingv1.NetworkPolicy{}, DenyPolicyName},
			{&networkingv1.NetworkPolicy{}, AllowPolicyName},
			{&rbacv1.RoleBinding{}, AdminBindingName},
		} {
			if err := gone(c.obj, c.name, "team-prune"); err != nil {
				return err
			}
		}
		var ns corev1.Namespace
		if err := exists(&ns, "team-prune", ""); err != nil {
			return err
		}
		if l := ns.Labels["pod-security.kubernetes.io/enforce"]; l != "baseline" {
			return fmt.Errorf("psa label = %q", l)
		}
		return nil
	})
	waitReady(t, "team-prune", metav1.ConditionTrue, ReasonProvisioned)
	if got := getTenant(t, "team-prune").Status.ManagedResources; len(got) != 1 {
		t.Fatalf("managedResources = %v", got)
	}
}

func TestUnmanagedObjectsAreNotPruned(t *testing.T) {
	create(t, newTenant("team-keep", func(tn *tenancyv1alpha1.Tenant) { tn.Spec.Quota = nil }))
	waitReady(t, "team-keep", metav1.ConditionTrue, ReasonProvisioned)
	// A quota with our name but created by a human (no managed-by label)
	// must survive reconciliation.
	create(t, &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: QuotaName, Namespace: "team-keep"},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("3")}},
	})
	tn := getTenant(t, "team-keep")
	tn.Spec.PodSecurity = "baseline" // bump generation → reconcile
	if err := k8s.Update(testCtx, tn); err != nil {
		t.Fatal(err)
	}
	waitReady(t, "team-keep", metav1.ConditionTrue, ReasonProvisioned)
	consistently(t, time.Second, func() error { return exists(&corev1.ResourceQuota{}, QuotaName, "team-keep") })
}

func TestNamespaceConflictIsNotAdopted(t *testing.T) {
	create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "legacy-ns", Labels: map[string]string{"owner": "someone-else"}}})
	create(t, newTenant("team-conflict", func(tn *tenancyv1alpha1.Tenant) { tn.Spec.Namespace = "legacy-ns" }))
	waitReady(t, "team-conflict", metav1.ConditionFalse, ReasonNamespaceConflict)

	var ns corev1.Namespace
	if err := exists(&ns, "legacy-ns", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := ns.Labels[LabelTenant]; ok || len(ns.OwnerReferences) != 0 {
		t.Fatalf("foreign namespace was adopted: labels=%v owners=%v", ns.Labels, ns.OwnerReferences)
	}
	if err := gone(&corev1.ResourceQuota{}, QuotaName, "legacy-ns"); err != nil {
		t.Fatal(err)
	}

	// Deleting a conflicted tenant must never delete the foreign namespace.
	if err := k8s.Delete(testCtx, getTenant(t, "team-conflict")); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error { return gone(&tenancyv1alpha1.Tenant{}, "team-conflict", "") })
	if err := exists(&corev1.Namespace{}, "legacy-ns", ""); err != nil {
		t.Fatalf("foreign namespace deleted: %v", err)
	}
}

func TestAPIValidation(t *testing.T) {
	create(t, newTenant("team-cel", func(tn *tenancyv1alpha1.Tenant) { tn.Spec.Namespace = "cel-ns" }))
	waitReady(t, "team-cel", metav1.ConditionTrue, ReasonProvisioned)

	tn := getTenant(t, "team-cel")
	tn.Spec.Namespace = "cel-ns-renamed"
	err := k8s.Update(testCtx, tn)
	if err == nil || !strings.Contains(err.Error(), "spec.namespace is immutable") {
		t.Fatalf("CEL immutability rule not enforced: %v", err)
	}

	tn = getTenant(t, "team-cel")
	tn.Spec.Namespace = ""
	if err := k8s.Update(testCtx, tn); err == nil {
		t.Fatal("clearing spec.namespace (which would re-target the tenant) was allowed")
	}

	for name, mut := range map[string]func(*tenancyv1alpha1.Tenant){
		"bad-policy": func(tn *tenancyv1alpha1.Tenant) { tn.Spec.DeletionPolicy = "Shred" },
		"bad-ns":     func(tn *tenancyv1alpha1.Tenant) { tn.Spec.Namespace = "Not_A_DNS_Label" },
		"bad-psa":    func(tn *tenancyv1alpha1.Tenant) { tn.Spec.PodSecurity = "yolo" },
	} {
		if err := k8s.Create(testCtx, newTenant("v-"+name, mut)); !apierrors.IsInvalid(err) {
			t.Errorf("%s: want Invalid, got %v", name, err)
		}
	}

	// Defaults are applied by the API server from the CRD schema.
	d := getTenant(t, "team-cel")
	if d.Spec.DeletionPolicy != tenancyv1alpha1.DeletionPolicyDelete || d.Spec.PodSecurity != "restricted" || !ptr.Deref(d.Spec.NetworkIsolation, false) {
		t.Fatalf("defaults not applied: %+v", d.Spec)
	}
}

func TestDeletionPolicyDeleteWaitsForNamespace(t *testing.T) {
	create(t, newTenant("team-del"))
	waitReady(t, "team-del", metav1.ConditionTrue, ReasonProvisioned)
	if err := k8s.Delete(testCtx, getTenant(t, "team-del")); err != nil {
		t.Fatal(err)
	}

	// envtest runs no namespace controller, so the namespace stays
	// Terminating — which lets us observe that the Tenant is held back by
	// its finalizer until the namespace is really gone.
	eventually(t, func() error {
		var ns corev1.Namespace
		if err := exists(&ns, "team-del", ""); err != nil {
			return err
		}
		if ns.DeletionTimestamp.IsZero() {
			return errors.New("namespace not being deleted yet")
		}
		return nil
	})
	consistently(t, time.Second, func() error { return exists(&tenancyv1alpha1.Tenant{}, "team-del", "") })
	if c := meta.FindStatusCondition(getTenant(t, "team-del").Status.Conditions, tenancyv1alpha1.ConditionReady); c == nil || c.Reason != ReasonDeleting {
		t.Fatalf("Ready condition during deletion: %+v", c)
	}

	// Play the namespace controller: clear spec.finalizers via /finalize.
	ns, err := clientset.CoreV1().Namespaces().Get(testCtx, "team-del", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns.Spec.Finalizers = nil
	if _, err := clientset.CoreV1().Namespaces().Finalize(testCtx, ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error { return gone(&tenancyv1alpha1.Tenant{}, "team-del", "") })
}

func TestDeletionPolicyRetainOrphansNamespace(t *testing.T) {
	create(t, newTenant("team-retain", func(tn *tenancyv1alpha1.Tenant) {
		tn.Spec.DeletionPolicy = tenancyv1alpha1.DeletionPolicyRetain
	}))
	waitReady(t, "team-retain", metav1.ConditionTrue, ReasonProvisioned)
	if err := k8s.Delete(testCtx, getTenant(t, "team-retain")); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error { return gone(&tenancyv1alpha1.Tenant{}, "team-retain", "") })

	var ns corev1.Namespace
	if err := exists(&ns, "team-retain", ""); err != nil {
		t.Fatal(err)
	}
	if !ns.DeletionTimestamp.IsZero() || len(ns.OwnerReferences) != 0 {
		t.Fatalf("retained namespace still owned/deleting: owners=%v deletion=%v", ns.OwnerReferences, ns.DeletionTimestamp)
	}
	if _, ok := ns.Labels[LabelTenant]; ok {
		t.Fatalf("tenant label left on orphaned namespace: %v", ns.Labels)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatal("orphaning must keep the security posture labels")
	}
}
