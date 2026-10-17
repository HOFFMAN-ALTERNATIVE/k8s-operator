/*
Copyright 2026 Herman Hoffman.

Licensed under the MIT License. See LICENSE in the project root.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletionPolicy decides what happens to the tenant namespace when the
// Tenant is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	// DeletionPolicyDelete deletes the namespace and everything in it.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyRetain orphans the namespace (ownership is released).
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// PodSecurityLevel is a Pod Security Standards level.
// +kubebuilder:validation:Enum=privileged;baseline;restricted
type PodSecurityLevel string

// TenantSpec is the desired state of a Tenant.
// +kubebuilder:validation:XValidation:rule="has(self.namespace) == has(oldSelf.namespace)",message="spec.namespace is immutable"
type TenantSpec struct {
	// Namespace to provision for the tenant. Defaults to the Tenant name.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.namespace is immutable"
	Namespace string `json:"namespace,omitempty"`

	// Admins are bound to the built-in "admin" ClusterRole inside the
	// tenant namespace.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	Admins []rbacv1.Subject `json:"admins,omitempty"`

	// Quota becomes the namespace ResourceQuota (hard limits).
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	Quota corev1.ResourceList `json:"quota,omitempty"`

	// DefaultLimits become a LimitRange default for containers that don't
	// declare their own requests/limits.
	// +optional
	DefaultLimits *ContainerDefaults `json:"defaultLimits,omitempty"`

	// NetworkIsolation installs default-deny ingress plus allow-same-
	// namespace NetworkPolicies.
	// +optional
	// +kubebuilder:default=true
	NetworkIsolation *bool `json:"networkIsolation,omitempty"`

	// PodSecurity is enforced through Pod Security Admission labels.
	// +optional
	// +kubebuilder:default=restricted
	PodSecurity PodSecurityLevel `json:"podSecurity,omitempty"`

	// DeletionPolicy controls the namespace's fate when the Tenant goes away.
	// +optional
	// +kubebuilder:default=Delete
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// ContainerDefaults are per-container defaults for a LimitRange.
type ContainerDefaults struct {
	// Requests applied when a container declares none.
	// +optional
	// +kubebuilder:validation:MaxProperties=16
	Requests corev1.ResourceList `json:"requests,omitempty"`
	// Limits applied when a container declares none.
	// +optional
	// +kubebuilder:validation:MaxProperties=16
	Limits corev1.ResourceList `json:"limits,omitempty"`
}

// Condition types.
const (
	// ConditionReady is True when every managed object matches the spec.
	ConditionReady = "Ready"
)

// TenantStatus is the observed state of a Tenant.
type TenantStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Namespace actually provisioned.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// ManagedResources lists kind/name of every object the operator owns.
	// +optional
	// +listType=atomic
	ManagedResources []string `json:"managedResources,omitempty"`
	// Conditions follow the Kubernetes API conventions.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// Tenant provisions an isolated, quota-bounded namespace for a team.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=tn
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Tenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantSpec   `json:"spec,omitempty"`
	Status TenantStatus `json:"status,omitempty"`
}

// TargetNamespace returns the namespace this Tenant provisions.
func (t *Tenant) TargetNamespace() string {
	if t.Spec.Namespace != "" {
		return t.Spec.Namespace
	}
	return t.Name
}

// IsolationEnabled reports whether NetworkPolicies should be installed.
func (t *Tenant) IsolationEnabled() bool {
	return t.Spec.NetworkIsolation == nil || *t.Spec.NetworkIsolation
}

// +kubebuilder:object:root=true

// TenantList is a list of Tenants.
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tenant `json:"items"`
}
