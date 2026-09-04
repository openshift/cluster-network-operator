package ingressconfig

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// mockRESTMapper is a fake RESTMapper for testing
type mockRESTMapper struct {
	shouldReturnError bool
	errorToReturn     error
}

func (m *mockRESTMapper) KindFor(_ schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}

func (m *mockRESTMapper) KindsFor(_ schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, nil
}

func (m *mockRESTMapper) ResourceFor(_ schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, nil
}

func (m *mockRESTMapper) ResourcesFor(_ schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, nil
}

func (m *mockRESTMapper) RESTMapping(_ schema.GroupKind, _ ...string) (*meta.RESTMapping, error) {
	if m.shouldReturnError {
		return nil, m.errorToReturn
	}
	// Return a successful mapping indicating CRD exists
	return &meta.RESTMapping{
		Resource: schema.GroupVersionResource{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "ingresscontrollers",
		},
	}, nil
}

func (m *mockRESTMapper) RESTMappings(_ schema.GroupKind, _ ...string) ([]*meta.RESTMapping, error) {
	return nil, nil
}

func (m *mockRESTMapper) ResourceSingularizer(_ string) (singular string, err error) {
	return "", nil
}

// mockClient wraps the fake client with a custom RESTMapper
type mockClient struct {
	crclient.Client
	restMapper meta.RESTMapper
}

func (m *mockClient) RESTMapper() meta.RESTMapper {
	return m.restMapper
}
