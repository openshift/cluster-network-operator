package k8s

import (
	"crypto/md5"
	"encoding/json"
	"fmt"

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

// CalculateHash computes MD5 sum of the JSONfied object passed as obj.
func CalculateHash(obj interface{}) (string, error) {
	configStr, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	configSum := md5.Sum(configStr)
	return fmt.Sprintf("%x", configSum), nil
}
