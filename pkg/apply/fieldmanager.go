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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/csaupgrade"
)

// mergeManager merges field manager ownership of fields from field manager named 'fromManager' to 'intoManager'.
// It will apply this for operation 'Apply' and 'Update'.
func mergeManager(ctx context.Context, clusterClient client.ClusterClient, us *unstructured.Unstructured, fromManager, intoManager string,
	objGVR schema.GroupVersionResource) error {
	var patch []byte
	var patchType types.PatchType
	var err error

	for _, m := range us.GetManagedFields() {
		if m.Manager != fromManager {
			continue
		}
		if m.Operation == metav1.ManagedFieldsOperationUpdate {
			patch, err = csaupgrade.UpgradeManagedFieldsPatch(us, map[string]sets.Empty{fromManager: {}}, intoManager)
			patchType = types.JSONPatchType
		} else if m.Operation == metav1.ManagedFieldsOperationApply {
			patch, err = runtime.Encode(unstructured.UnstructuredJSONScheme, getEmptyUS(us.GetAPIVersion(), us.GetKind(),
				us.GetName(), us.GetNamespace()))
			patchType = types.ApplyPatchType
		} else {
			return fmt.Errorf("unexpected operation found for object %v: %s", objGVR.String(), m.Operation)
		}
		if err != nil {
			return fmt.Errorf("failed to create patch (type %s) for object %s %s: %v", patchType, objGVR.String(), us.GetName(), err)
		}
		if len(patch) > 0 {
			_, err = clusterClient.Dynamic().Resource(objGVR).Namespace(us.GetNamespace()).Patch(ctx, us.GetName(), patchType,
				patch, metav1.PatchOptions{FieldManager: intoManager})
			if err != nil {
				return fmt.Errorf("failed to patch (type %s) for object %s %s: %v", patchType, objGVR.String(), us.GetName(), err)
			}
		}
	}
	return nil
}

func doesManagerOpExist(mfs []metav1.ManagedFieldsEntry, managerName string, ops ...metav1.ManagedFieldsOperationType) bool {
	for _, mf := range mfs {
		if mf.Manager == managerName {
			for _, op := range ops {
				if mf.Operation == op {
					return true
				}
			}
		}
	}
	return false
}

func getEmptyUS(apiVer, kind, name, ns string) *unstructured.Unstructured {
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
