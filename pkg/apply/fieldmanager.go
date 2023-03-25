package apply

import (
	"context"
	"fmt"
	"github.com/openshift/cluster-network-operator/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// depreciateFieldManager merges field manager ownership of fields from field manager named 'fromFMName' to 'intoFMName'.
// If a field manager with name 'intoFMName' does not exist, it is created.
func depreciateFieldManager(ctx context.Context, clusterClient client.ClusterClient, depFieldManagerName string,
	objManagedFields []metav1.ManagedFieldsEntry, objAPIVer, objKind, objName, objNamespace string, objGVR schema.GroupVersionResource) error {

	for _, m := range objManagedFields {
		if m.Manager != depFieldManagerName {
			continue
		}
		if m.Operation == metav1.ManagedFieldsOperationUpdate {
			if _, err := clusterClient.Dynamic().Resource(objGVR).Namespace(objNamespace).Update(ctx, getUnstructuredObjWithNoSpec(objAPIVer, objKind,
				objName, objNamespace), metav1.UpdateOptions{FieldManager: depFieldManagerName}); err != nil {
				return err
			}
		}
		if m.Operation == metav1.ManagedFieldsOperationApply {
			patchOptions := metav1.PatchOptions{
				FieldManager: depFieldManagerName,
			}
			data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, getUnstructuredObjWithNoSpec(objAPIVer, objKind, objName, objNamespace))
			if err != nil {
				return fmt.Errorf("failed to encode: %w", err)
			}
			if _, err := clusterClient.Dynamic().Resource(objGVR).Namespace(objNamespace).Patch(ctx, objName, types.ApplyPatchType,
				data, patchOptions); err != nil {
				return fmt.Errorf("failed to patch object: %w", err)
			}
		}
	}
	return nil
}

func getUnstructuredObjWithNoSpec(apiVer, kind, name, ns string) *unstructured.Unstructured {
	data := []byte(fmt.Sprintf(`
apiVersion: %s
kind: %s
metadata:
  name: %s
  namespace: %s
`, apiVer, kind, name, ns))
	newObj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if err := yaml.Unmarshal(data, &newObj.Object); err != nil {
		panic(fmt.Sprintf("failed to unmarshal: %v", err))
	}
	return newObj
}

func managerOpExists(mfs []metav1.ManagedFieldsEntry, managerName string, op metav1.ManagedFieldsOperationType) bool {
	for _, mf := range mfs {
		if mf.Manager == managerName && mf.Operation == op {
			return true
		}
	}
	return false
}
