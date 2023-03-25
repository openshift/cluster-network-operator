package apply

import (
	"context"
	"fmt"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"log"
	"reflect"
	"sigs.k8s.io/yaml"
	"testing"
)

var (
	testNS          = "default"
	fromManagerName = "cluster-network-operator"
	toManagerName   = "cluster-network-operator/operconfig"
	applyOpName     = "Apply"
	updateOpName    = "Update"
)

func getObjOwningVolumeByManager(t *testing.T, manager, op, ns string) *unstructured.Unstructured {
	data := []byte(fmt.Sprintf(`
apiVersion: apps/v1
kind: DaemonSet
metadata:  
  managedFields:
  - apiVersion: apps/v1
    fieldsType: FieldsV1
    fieldsV1:      
      f:spec:        
        f:template:          
          f:spec:
            f:containers:              
              k:{"name":"cno-mf-test"}:
                .: {}
                f:command: {}                
                f:image: {}                
                f:name: {}                
                f:volumeMounts:                  
                  k:{"mountPath":"/lib/modules"}:
                    .: {}
                    f:mountPath: {}
                    f:name: {}
                    f:readOnly: {}                            
            f:volumes:              
              k:{"name":"host-modules"}:
                .: {}
                f:hostPath:
                  f:path: {}
                f:name: {}                 
    manager: %s
    operation: %s
    time: "2023-03-27T13:11:13Z"  
  name: cno-mf-test
  namespace: %s
spec:
  selector:
    matchLabels:
      app: cno-mf
  template:
    metadata:
      labels:
        app: cno-mf
    spec:
      containers:
      - command:
        - /bin/bash
        - -c
        - |
          #!/bin/bash
          set -euo pipefail
          sleep 200000
        image: registry.ci.openshift.org/openshift/origin-v4.0:base
        imagePullPolicy: IfNotPresent        
        name: cno-mf-test
        volumeMounts:
        - mountPath: /lib/modules
          name: host-modules
          readOnly: true                    
      volumes:      
      - hostPath:
          path: /lib/modules
          type: ""
        name: host-modules
`, manager, op, ns))
	newObj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if err := yaml.Unmarshal(data, &newObj.Object); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	return newObj
}

func getSingleManager(t *testing.T, manager, op, ns string) *unstructured.Unstructured {
	data := []byte(fmt.Sprintf(`
apiVersion: apps/v1
kind: DaemonSet
metadata:  
  managedFields:
  - apiVersion: apps/v1
    fieldsType: FieldsV1
    fieldsV1:      
      f:spec:        
        f:template:          
          f:spec:
            f:containers:              
              k:{"name":"cno-mf-test"}:
                .: {}
                f:command: {}                
                f:image: {}                
                f:name: {}                
                f:volumeMounts:                  
                  k:{"mountPath":"/lib/modules"}:
                    .: {}
                    f:mountPath: {}
                    f:name: {}
                    f:readOnly: {}
                  k:{"mountPath":"/run/netns"}:
                    .: {}
                    f:mountPath: {}
                    f:name: {}
                    f:readOnly: {}  
            f:volumes:              
              k:{"name":"host-modules"}:
                .: {}
                f:hostPath:
                  f:path: {}
                f:name: {}
              k:{"name":"host-run-netns"}:
                .: {}
                f:hostPath:
                  f:path: {}
                f:name: {}   
    manager: %s
    operation: %s
    time: "2023-03-27T13:11:13Z"  
  name: cno-mf-test
  namespace: %s
spec:
  selector:
    matchLabels:
      app: cno-mf
  template:
    metadata:
      labels:
        app: cno-mf
    spec:
      containers:
      - command:
        - /bin/bash
        - -c
        - |
          #!/bin/bash
          set -euo pipefail
          sleep 200000
        image: registry.ci.openshift.org/openshift/origin-v4.0:base
        imagePullPolicy: IfNotPresent        
        name: cno-mf-test
        volumeMounts:
        - mountPath: /lib/modules
          name: host-modules
          readOnly: true                    
      volumes:      
      - hostPath:
          path: /lib/modules
          type: ""
        name: host-modules
      - name: host-run-netns
        hostPath:
          path: /run/netns
`, manager, op, ns))
	newObj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if err := yaml.Unmarshal(data, &newObj.Object); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	return newObj
}

func getMultiManagerNonOverlap(t *testing.T, managerOne, opOne, managerTwo, OpTwo, ns string) *unstructured.Unstructured {
	data := []byte(fmt.Sprintf(`
apiVersion: apps/v1
kind: DaemonSet
metadata:  
  managedFields:
  - apiVersion: apps/v1
    fieldsType: FieldsV1
    fieldsV1:      
      f:spec:        
        f:template:          
          f:spec:
            f:containers:              
              k:{"name":"cno-mf-test"}:
                .: {}
                f:command: {}                
                f:image: {}                
                f:name: {}                
                f:volumeMounts:                  
                  k:{"mountPath":"/lib/modules"}:
                    .: {}
                    f:mountPath: {}
                    f:name: {}
                    f:readOnly: {}  
            f:volumes:              
              k:{"name":"host-modules"}:
                .: {}
                f:hostPath:
                  f:path: {}
                f:name: {}
    manager: %s
    operation: %s
    time: "2023-03-27T13:11:13Z"
  - apiVersion: apps/v1
    fieldsType: FieldsV1
    fieldsV1:      
      f:spec:        
        f:template:          
          f:spec:
            f:containers:              
              k:{"name":"cno-mf-test"}:
                .: {}
                f:command: {}                
                f:image: {}                
                f:name: {}                
                f:volumeMounts:
                  k:{"mountPath":"/run/netns"}:
                    .: {}
                    f:mountPath: {}
                    f:name: {}
                    f:readOnly: {}  
            f:volumes:
              k:{"name":"host-run-netns"}:
                .: {}
                f:hostPath:
                  f:path: {}
                f:name: {}   
    manager: %s
    operation: %s
    time: "2023-03-27T13:11:13Z"  
  name: cno-mf-test
  namespace: %s
spec:
  selector:
    matchLabels:
      app: cno-mf
  template:
    metadata:
      labels:
        app: cno-mf
    spec:
      containers:
      - command:
        - /bin/bash
        - -c
        - |
          #!/bin/bash
          set -euo pipefail
          sleep 200000
        image: registry.ci.openshift.org/openshift/origin-v4.0:base
        imagePullPolicy: IfNotPresent        
        name: cno-mf-test
        volumeMounts:
        - mountPath: /lib/modules
          name: host-modules
          readOnly: true                    
      volumes:      
      - hostPath:
          path: /lib/modules
          type: ""
        name: host-modules
      - name: host-run-netns
        hostPath:
          path: /run/netns
`, managerOne, opOne, managerTwo, OpTwo, ns))
	newObj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if err := yaml.Unmarshal(data, &newObj.Object); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	return newObj
}

func Test_fieldmanager(t *testing.T) {
	tests := []struct {
		name    string
		input   *unstructured.Unstructured
		oldName string
		newName string
		output  *unstructured.Unstructured
	}{
		{
			"convert a field manager to new manager that doesn't exist",
			getSingleManager(t, fromManagerName, updateOpName, testNS),
			fromManagerName,
			toManagerName,
			getSingleManager(t, toManagerName, applyOpName, testNS),
		},
		{
			"convert a field manager to a manager that exists",
			getMultiManagerNonOverlap(t, fromManagerName, updateOpName, toManagerName, applyOpName, testNS),
			fromManagerName,
			toManagerName,
			getSingleManager(t, toManagerName, applyOpName, testNS),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewFakeClient(tt.input)
			client.Start(context.TODO())
			clusterClient := client.ClientFor(GetClusterName(tt.input))

			if clusterClient == nil {
				t.Fatalf("object %s/%s specifies unknown cluster %s", tt.input.GetNamespace(), tt.input.GetName(),
					GetClusterName(tt.input))
			}
			// determine resource
			gvr := schema.GroupVersionResource{
				Group:    "apps/v1",
				Version:  "Deployment",
				Resource: "apps",
			}

			if _, err := clusterClient.Dynamic().Resource(gvr).Namespace(testNS).Create(context.TODO(), tt.input,
				metav1.CreateOptions{}); err != nil {
				t.Fatalf("failed to create obj: %v", err)
			}

			defer func() {
				if err := clusterClient.Dynamic().Resource(gvr).Namespace(testNS).Delete(context.TODO(), tt.input.GetName(),
					metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
					t.Errorf("while cleaning up resource %+v, encourtered error: %v", gvr, err)
				}
			}()

			mergeResult, err := mergeFieldOwners(context.TODO(), clusterClient, tt.oldName, tt.newName, tt.input.GetManagedFields(),
				tt.input.GetName(), tt.input.GetNamespace(), gvr)
			if err != nil {
				t.Fatalf("failed to rename field manager: %v", err)
			}
			if tt.output == nil && mergeResult == nil {
				return
			}

			if tt.output != nil && mergeResult == nil {
				t.Fatalf("Expected %+v\nGot nil", tt.output)
			}

			if tt.output == nil && mergeResult != nil {
				t.Fatalf("Expected nil\nGot %+v", mergeResult)
			}

			ds, err := clusterClient.Dynamic().Resource(gvr).Namespace(testNS).Get(context.TODO(), tt.input.GetName(), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("failed to get resource: %v", err)
			}

			log.Printf("After update, MF are: %+v", ds.GetManagedFields())

			if !reflect.DeepEqual(tt.output.GetManagedFields(), mergeResult.GetManagedFields()) {
				t.Fatalf("Expected outputs to be equal but they are not: expected\n%+v\nto equal:\n%+v",
					tt.output.GetManagedFields(), mergeResult.GetManagedFields())
			}
		})
	}
}
