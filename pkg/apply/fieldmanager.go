package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/openshift/cluster-network-operator/pkg/client"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
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
			patch, err = UpgradeManagedFieldsPatch(us, map[string]sets.Empty{fromManager: {}}, intoManager)
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

// Finds all managed fields owners of the given operation type which owns all of
// the fields in the given set
//
// If there is an error decoding one of the fieldsets for any reason, it is ignored
// and assumed not to match the query.
func FindFieldsOwners(
	managedFields []metav1.ManagedFieldsEntry,
	operation metav1.ManagedFieldsOperationType,
	fields *fieldpath.Set,
) []metav1.ManagedFieldsEntry {
	var result []metav1.ManagedFieldsEntry
	for _, entry := range managedFields {
		if entry.Operation != operation {
			continue
		}

		fieldSet, err := decodeManagedFieldsEntrySet(entry)
		if err != nil {
			continue
		}

		if fields.Difference(&fieldSet).Empty() {
			result = append(result, entry)
		}
	}
	return result
}

// Upgrades the Manager information for fields managed with client-side-apply (CSA)
// Prepares fields owned by `csaManager` for 'Update' operations for use now
// with the given `ssaManager` for `Apply` operations.
//
// This transformation should be performed on an object if it has been previously
// managed using client-side-apply to prepare it for future use with
// server-side-apply.
//
// Caveats:
//  1. This operation is not reversible. Information about which fields the client
//     owned will be lost in this operation.
//  2. Supports being performed either before or after initial server-side apply.
//  3. Client-side apply tends to own more fields (including fields that are defaulted),
//     this will possibly remove this defaults, they will be re-defaulted, that's fine.
//  4. Care must be taken to not overwrite the managed fields on the server if they
//     have changed before sending a patch.
//
// obj - Target of the operation which has been managed with CSA in the past
// csaManagerNames - Names of FieldManagers to merge into ssaManagerName
// ssaManagerName - Name of FieldManager to be used for `Apply` operations
func UpgradeManagedFields(
	obj runtime.Object,
	csaManagerNames map[string]sets.Empty,
	ssaManagerName string,
) error {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return err
	}

	filteredManagers := accessor.GetManagedFields()

	for csaManagerName := range csaManagerNames {
		filteredManagers, err = upgradedManagedFields(
			filteredManagers, csaManagerName, ssaManagerName)

		if err != nil {
			return err
		}
	}

	// Commit changes to object
	accessor.SetManagedFields(filteredManagers)
	return nil
}

// Calculates a minimal JSON Patch to send to upgrade managed fields
// See `UpgradeManagedFields` for more information.
//
// obj - Target of the operation which has been managed with CSA in the past
// csaManagerNames - Names of FieldManagers to merge into ssaManagerName
// ssaManagerName - Name of FieldManager to be used for `Apply` operations
//
// Returns non-nil error if there was an error, a JSON patch, or nil bytes if
// there is no work to be done.
func UpgradeManagedFieldsPatch(
	obj runtime.Object,
	csaManagerNames map[string]sets.Empty,
	ssaManagerName string) ([]byte, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}

	managedFields := accessor.GetManagedFields()
	filteredManagers := accessor.GetManagedFields()
	for csaManagerName := range csaManagerNames {
		filteredManagers, err = upgradedManagedFields(
			filteredManagers, csaManagerName, ssaManagerName)
		if err != nil {
			return nil, err
		}
	}

	if reflect.DeepEqual(managedFields, filteredManagers) {
		// If the managed fields have not changed from the transformed version,
		// there is no patch to perform
		return nil, nil
	}

	// Create a patch with a diff between old and new objects.
	// Just include all managed fields since that is only thing that will change
	//
	// Also include test for RV to avoid race condition
	jsonPatch := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/metadata/managedFields",
			"value": filteredManagers,
		},
		{
			// Use "replace" instead of "test" operation so that etcd rejects with
			// 409 conflict instead of apiserver with an invalid request
			"op":    "replace",
			"path":  "/metadata/resourceVersion",
			"value": accessor.GetResourceVersion(),
		},
	}

	return json.Marshal(jsonPatch)
}

// Returns a copy of the provided managed fields that has been migrated from
// client-side-apply to server-side-apply, or an error if there was an issue
func upgradedManagedFields(
	managedFields []metav1.ManagedFieldsEntry,
	csaManagerName string,
	ssaManagerName string,
) ([]metav1.ManagedFieldsEntry, error) {
	if managedFields == nil {
		return nil, nil
	}

	// Create managed fields clone since we modify the values
	managedFieldsCopy := make([]metav1.ManagedFieldsEntry, len(managedFields))
	if copy(managedFieldsCopy, managedFields) != len(managedFields) {
		return nil, errors.New("failed to copy managed fields")
	}
	managedFields = managedFieldsCopy

	// Locate SSA manager
	replaceIndex, managerExists := findFirstIndex(managedFields,
		func(entry metav1.ManagedFieldsEntry) bool {
			return entry.Manager == ssaManagerName &&
				entry.Operation == metav1.ManagedFieldsOperationApply &&
				entry.Subresource == ""
		})

	if !managerExists {
		// SSA manager does not exist. Find the most recent matching CSA manager,
		// convert it to an SSA manager.
		//
		// (find first index, since managed fields are sorted so that most recent is
		//  first in the list)
		replaceIndex, managerExists = findFirstIndex(managedFields,
			func(entry metav1.ManagedFieldsEntry) bool {
				return entry.Manager == csaManagerName &&
					entry.Operation == metav1.ManagedFieldsOperationUpdate &&
					entry.Subresource == ""
			})

		if !managerExists {
			// There are no CSA managers that need to be converted. Nothing to do
			// Return early
			return managedFields, nil
		}

		// Convert CSA manager into SSA manager
		managedFields[replaceIndex].Operation = metav1.ManagedFieldsOperationApply
		managedFields[replaceIndex].Manager = ssaManagerName
	}
	err := unionManagerIntoIndex(managedFields, replaceIndex, csaManagerName)
	if err != nil {
		return nil, err
	}

	// Create version of managed fields which has no CSA managers with the given name
	filteredManagers := filter(managedFields, func(entry metav1.ManagedFieldsEntry) bool {
		return !(entry.Manager == csaManagerName &&
			entry.Operation == metav1.ManagedFieldsOperationUpdate &&
			entry.Subresource == "")
	})

	return filteredManagers, nil
}

// Locates an Update manager entry named `csaManagerName` with the same APIVersion
// as the manager at the targetIndex. Unions both manager's fields together
// into the manager specified by `targetIndex`. No other managers are modified.
func unionManagerIntoIndex(
	entries []metav1.ManagedFieldsEntry,
	targetIndex int,
	csaManagerName string,
) error {
	ssaManager := entries[targetIndex]

	// find Update manager of same APIVersion, union ssa fields with it.
	// discard all other Update managers of the same name
	csaManagerIndex, csaManagerExists := findFirstIndex(entries,
		func(entry metav1.ManagedFieldsEntry) bool {
			return entry.Manager == csaManagerName &&
				entry.Operation == metav1.ManagedFieldsOperationUpdate &&
				//!TODO: some users may want to migrate subresources.
				// should thread through the args at some point.
				entry.Subresource == "" &&
				entry.APIVersion == ssaManager.APIVersion
		})

	targetFieldSet, err := decodeManagedFieldsEntrySet(ssaManager)
	if err != nil {
		return fmt.Errorf("failed to convert fields to set: %w", err)
	}

	combinedFieldSet := &targetFieldSet

	// Union the csa manager with the existing SSA manager. Do nothing if
	// there was no good candidate found
	if csaManagerExists {
		csaManager := entries[csaManagerIndex]

		csaFieldSet, err := decodeManagedFieldsEntrySet(csaManager)
		if err != nil {
			return fmt.Errorf("failed to convert fields to set: %w", err)
		}

		combinedFieldSet = combinedFieldSet.Union(&csaFieldSet)
	}

	// Encode the fields back to the serialized format
	err = encodeManagedFieldsEntrySet(&entries[targetIndex], *combinedFieldSet)
	if err != nil {
		return fmt.Errorf("failed to encode field set: %w", err)
	}

	return nil
}

func findFirstIndex[T any](
	collection []T,
	predicate func(T) bool,
) (int, bool) {
	for idx, entry := range collection {
		if predicate(entry) {
			return idx, true
		}
	}

	return -1, false
}

func filter[T any](
	collection []T,
	predicate func(T) bool,
) []T {
	result := make([]T, 0, len(collection))

	for _, value := range collection {
		if predicate(value) {
			result = append(result, value)
		}
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// Included from fieldmanager.internal to avoid dependency cycle
// FieldsToSet creates a set paths from an input trie of fields
func decodeManagedFieldsEntrySet(f metav1.ManagedFieldsEntry) (s fieldpath.Set, err error) {
	err = s.FromJSON(bytes.NewReader(f.FieldsV1.Raw))
	return s, err
}

// SetToFields creates a trie of fields from an input set of paths
func encodeManagedFieldsEntrySet(f *metav1.ManagedFieldsEntry, s fieldpath.Set) (err error) {
	f.FieldsV1.Raw, err = s.ToJSON()
	return err
}
