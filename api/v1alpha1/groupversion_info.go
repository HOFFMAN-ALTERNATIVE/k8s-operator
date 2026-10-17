/*
Copyright 2026 Herman Hoffman.

Licensed under the MIT License. See LICENSE in the project root.
*/

// Package v1alpha1 contains the Tenant API.
// +kubebuilder:object:generate=true
// +groupName=tenancy.hoffman-alternative.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the API group and version of this package.
	GroupVersion = schema.GroupVersion{Group: "tenancy.hoffman-alternative.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a runtime.Scheme. The API
	// package depends only on apimachinery so clients can import it cheaply.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &Tenant{}, &TenantList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
