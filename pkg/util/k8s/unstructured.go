package k8s

import (
	"encoding/json"

	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ToUnstructured convers an arbitrary object (which MUST obey the
// k8s object conventions) to an Unstructured
func ToUnstructured(obj interface{}) (*uns.Unstructured, error) {
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to convert to unstructured (marshal)")
	}
	u := &uns.Unstructured{}
	if err := json.Unmarshal(b, u); err != nil {
		return nil, errors.Wrapf(err, "failed to convert to unstructured (unmarshal)")
	}
	return u, nil
}
