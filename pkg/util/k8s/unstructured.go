package k8s

import (
	"crypto/md5"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// ToUnstructured converts an arbitrary object (which MUST obey the
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

// Same returns true if two objects are the "same" - that is to say, they
// would point to the same object in the apiserver. Specifically, they have the same
// group, kind, namespace, and name
func Same(obj1, obj2 *uns.Unstructured) bool {
	return (obj1.GroupVersionKind().GroupKind() == obj2.GroupVersionKind().GroupKind() &&
		obj1.GetNamespace() == obj2.GetNamespace() &&
		obj1.GetName() == obj2.GetName())
}

// ReplaceObj will replace a given object in a list of objects with another.
// It will match on the object's group, kind, and identity. If the object isn't found
// in the list, `new` will not be added.
func ReplaceObj(objs []*uns.Unstructured, new *uns.Unstructured) []*uns.Unstructured {
	out := make([]*uns.Unstructured, 0, len(objs))

	replaced := false
	for _, obj := range objs {
		// if the object in the list
		if Same(obj, new) {
			out = append(out, new)
			replaced = true
		} else {
			out = append(out, obj)
		}
	}

	if !replaced {
		klog.V(3).Infof("Warning: ReplaceObj() didn't find replacement for %s %s/%s, skipping",
			new.GroupVersionKind().GroupKind(), new.GetNamespace(), new.GetName())
	}

	return out
}

// RemoveObjByGroupKindName will remove a given object from the list of objects.
// It will match on the object's kind, namespace and name. If the object isn't found
// in the list, it will not be removed.
func RemoveObjByGroupKindName(objs []*uns.Unstructured, group, kind, namespace, name string) []*uns.Unstructured {
	out := make([]*uns.Unstructured, 0, len(objs))

	removed := false
	for _, obj := range objs {
		// if the object in the list
		if (obj.GroupVersionKind().GroupKind() == schema.GroupKind{Group: group, Kind: kind} &&
			obj.GetNamespace() == namespace && obj.GetName() == name) {
			removed = true
		} else {
			out = append(out, obj)
		}
	}

	if !removed {
		klog.V(3).Infof("Warning: RemoveObjByKindName() didn't find the object to be removed for %s/%s %s/%s, skipping",
			group, kind, namespace, name)
	}

	return out
}
