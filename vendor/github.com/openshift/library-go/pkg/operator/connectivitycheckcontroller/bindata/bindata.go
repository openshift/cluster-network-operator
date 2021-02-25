// Code generated for package bindata by go-bindata DO NOT EDIT. (@generated)
// sources:
// pkg/operator/connectivitycheckcontroller/manifests/controlplane.operator.openshift.io_podnetworkconnectivitychecks.yaml
package bindata

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type asset struct {
	bytes []byte
	info  os.FileInfo
}

type bindataFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

// Name return file name
func (fi bindataFileInfo) Name() string {
	return fi.name
}

// Size return file size
func (fi bindataFileInfo) Size() int64 {
	return fi.size
}

// Mode return file mode
func (fi bindataFileInfo) Mode() os.FileMode {
	return fi.mode
}

// Mode return file modify time
func (fi bindataFileInfo) ModTime() time.Time {
	return fi.modTime
}

// IsDir return file whether a directory
func (fi bindataFileInfo) IsDir() bool {
	return fi.mode&os.ModeDir != 0
}

// Sys return file is sys mode
func (fi bindataFileInfo) Sys() interface{} {
	return nil
}

var _pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYaml = []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    include.release.openshift.io/self-managed-high-availability: "true"
    include.release.openshift.io/single-node-developer: "true"
  name: podnetworkconnectivitychecks.controlplane.operator.openshift.io
spec:
  group: controlplane.operator.openshift.io
  names:
    kind: PodNetworkConnectivityCheck
    listKind: PodNetworkConnectivityCheckList
    plural: podnetworkconnectivitychecks
    singular: podnetworkconnectivitycheck
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    subresources:
      status: {}
    schema:
      openAPIV3Schema:
        description: PodNetworkConnectivityCheck
        type: object
        required:
        - spec
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation
              of an object. Servers should convert recognized schemas to the latest
              internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this
              object represents. Servers may infer this from the endpoint the client
              submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          spec:
            description: Spec defines the source and target of the connectivity check
            type: object
            required:
            - sourcePod
            - targetEndpoint
            properties:
              sourcePod:
                description: SourcePod names the pod from which the condition will
                  be checked
                type: string
                pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$
              targetEndpoint:
                description: EndpointAddress to check. A TCP address of the form host:port.
                  Note that if host is a DNS name, then the check would fail if the
                  DNS name cannot be resolved. Specify an IP address for host to bypass
                  DNS name lookup.
                type: string
                pattern: ^\S+:\d*$
              tlsClientCert:
                description: TLSClientCert, if specified, references a kubernetes.io/tls
                  type secret with 'tls.crt' and 'tls.key' entries containing an optional
                  TLS client certificate and key to be used when checking endpoints
                  that require a client certificate in order to gracefully preform
                  the scan without causing excessive logging in the endpoint process.
                  The secret must exist in the same namespace as this resource.
                type: object
                required:
                - name
                properties:
                  name:
                    description: name is the metadata.name of the referenced secret
                    type: string
          status:
            description: Status contains the observed status of the connectivity check
            type: object
            properties:
              conditions:
                description: Conditions summarize the status of the check
                type: array
                items:
                  description: PodNetworkConnectivityCheckCondition represents the
                    overall status of the pod network connectivity.
                  type: object
                  required:
                  - lastTransitionTime
                  - status
                  - type
                  properties:
                    lastTransitionTime:
                      description: Last time the condition transitioned from one status
                        to another.
                      type: string
                      format: date-time
                      nullable: true
                    message:
                      description: Message indicating details about last transition
                        in a human readable format.
                      type: string
                    reason:
                      description: Reason for the condition's last status transition
                        in a machine readable format.
                      type: string
                    status:
                      description: Status of the condition
                      type: string
                    type:
                      description: Type of the condition
                      type: string
              failures:
                description: Failures contains logs of unsuccessful check actions
                type: array
                items:
                  description: LogEntry records events
                  type: object
                  required:
                  - success
                  - time
                  properties:
                    latency:
                      description: Latency records how long the action mentioned in
                        the entry took.
                      type: string
                      nullable: true
                    message:
                      description: Message explaining status in a human readable format.
                      type: string
                    reason:
                      description: Reason for status in a machine readable format.
                      type: string
                    success:
                      description: Success indicates if the log entry indicates a
                        success or failure.
                      type: boolean
                    time:
                      description: Start time of check action.
                      type: string
                      format: date-time
                      nullable: true
              outages:
                description: Outages contains logs of time periods of outages
                type: array
                items:
                  description: OutageEntry records time period of an outage
                  type: object
                  required:
                  - start
                  properties:
                    end:
                      description: End of outage detected
                      type: string
                      format: date-time
                      nullable: true
                    endLogs:
                      description: EndLogs contains log entries related to the end
                        of this outage. Should contain the success entry that resolved
                        the outage and possibly a few of the failure log entries that
                        preceded it.
                      type: array
                      items:
                        description: LogEntry records events
                        type: object
                        required:
                        - success
                        - time
                        properties:
                          latency:
                            description: Latency records how long the action mentioned
                              in the entry took.
                            type: string
                            nullable: true
                          message:
                            description: Message explaining status in a human readable
                              format.
                            type: string
                          reason:
                            description: Reason for status in a machine readable format.
                            type: string
                          success:
                            description: Success indicates if the log entry indicates
                              a success or failure.
                            type: boolean
                          time:
                            description: Start time of check action.
                            type: string
                            format: date-time
                            nullable: true
                    message:
                      description: Message summarizes outage details in a human readable
                        format.
                      type: string
                    start:
                      description: Start of outage detected
                      type: string
                      format: date-time
                      nullable: true
                    startLogs:
                      description: StartLogs contains log entries related to the start
                        of this outage. Should contain the original failure, any entries
                        where the failure mode changed.
                      type: array
                      items:
                        description: LogEntry records events
                        type: object
                        required:
                        - success
                        - time
                        properties:
                          latency:
                            description: Latency records how long the action mentioned
                              in the entry took.
                            type: string
                            nullable: true
                          message:
                            description: Message explaining status in a human readable
                              format.
                            type: string
                          reason:
                            description: Reason for status in a machine readable format.
                            type: string
                          success:
                            description: Success indicates if the log entry indicates
                              a success or failure.
                            type: boolean
                          time:
                            description: Start time of check action.
                            type: string
                            format: date-time
                            nullable: true
              successes:
                description: Successes contains logs successful check actions
                type: array
                items:
                  description: LogEntry records events
                  type: object
                  required:
                  - success
                  - time
                  properties:
                    latency:
                      description: Latency records how long the action mentioned in
                        the entry took.
                      type: string
                      nullable: true
                    message:
                      description: Message explaining status in a human readable format.
                      type: string
                    reason:
                      description: Reason for status in a machine readable format.
                      type: string
                    success:
                      description: Success indicates if the log entry indicates a
                        success or failure.
                      type: boolean
                    time:
                      description: Start time of check action.
                      type: string
                      format: date-time
                      nullable: true
`)

func pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYamlBytes() ([]byte, error) {
	return _pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYaml, nil
}

func pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYaml() (*asset, error) {
	bytes, err := pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYamlBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "pkg/operator/connectivitycheckcontroller/manifests/controlplane.operator.openshift.io_podnetworkconnectivitychecks.yaml", size: 0, mode: os.FileMode(0), modTime: time.Unix(0, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

// Asset loads and returns the asset for the given name.
// It returns an error if the asset could not be found or
// could not be loaded.
func Asset(name string) ([]byte, error) {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	if f, ok := _bindata[cannonicalName]; ok {
		a, err := f()
		if err != nil {
			return nil, fmt.Errorf("Asset %s can't read by error: %v", name, err)
		}
		return a.bytes, nil
	}
	return nil, fmt.Errorf("Asset %s not found", name)
}

// MustAsset is like Asset but panics when Asset would return an error.
// It simplifies safe initialization of global variables.
func MustAsset(name string) []byte {
	a, err := Asset(name)
	if err != nil {
		panic("asset: Asset(" + name + "): " + err.Error())
	}

	return a
}

// AssetInfo loads and returns the asset info for the given name.
// It returns an error if the asset could not be found or
// could not be loaded.
func AssetInfo(name string) (os.FileInfo, error) {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	if f, ok := _bindata[cannonicalName]; ok {
		a, err := f()
		if err != nil {
			return nil, fmt.Errorf("AssetInfo %s can't read by error: %v", name, err)
		}
		return a.info, nil
	}
	return nil, fmt.Errorf("AssetInfo %s not found", name)
}

// AssetNames returns the names of the assets.
func AssetNames() []string {
	names := make([]string, 0, len(_bindata))
	for name := range _bindata {
		names = append(names, name)
	}
	return names
}

// _bindata is a table, holding each asset generator, mapped to its name.
var _bindata = map[string]func() (*asset, error){
	"pkg/operator/connectivitycheckcontroller/manifests/controlplane.operator.openshift.io_podnetworkconnectivitychecks.yaml": pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYaml,
}

// AssetDir returns the file names below a certain
// directory embedded in the file by go-bindata.
// For example if you run go-bindata on data/... and data contains the
// following hierarchy:
//     data/
//       foo.txt
//       img/
//         a.png
//         b.png
// then AssetDir("data") would return []string{"foo.txt", "img"}
// AssetDir("data/img") would return []string{"a.png", "b.png"}
// AssetDir("foo.txt") and AssetDir("notexist") would return an error
// AssetDir("") will return []string{"data"}.
func AssetDir(name string) ([]string, error) {
	node := _bintree
	if len(name) != 0 {
		cannonicalName := strings.Replace(name, "\\", "/", -1)
		pathList := strings.Split(cannonicalName, "/")
		for _, p := range pathList {
			node = node.Children[p]
			if node == nil {
				return nil, fmt.Errorf("Asset %s not found", name)
			}
		}
	}
	if node.Func != nil {
		return nil, fmt.Errorf("Asset %s not found", name)
	}
	rv := make([]string, 0, len(node.Children))
	for childName := range node.Children {
		rv = append(rv, childName)
	}
	return rv, nil
}

type bintree struct {
	Func     func() (*asset, error)
	Children map[string]*bintree
}

var _bintree = &bintree{nil, map[string]*bintree{
	"pkg": {nil, map[string]*bintree{
		"operator": {nil, map[string]*bintree{
			"connectivitycheckcontroller": {nil, map[string]*bintree{
				"manifests": {nil, map[string]*bintree{
					"controlplane.operator.openshift.io_podnetworkconnectivitychecks.yaml": {pkgOperatorConnectivitycheckcontrollerManifestsControlplaneOperatorOpenshiftIo_podnetworkconnectivitychecksYaml, map[string]*bintree{}},
				}},
			}},
		}},
	}},
}}

// RestoreAsset restores an asset under the given directory
func RestoreAsset(dir, name string) error {
	data, err := Asset(name)
	if err != nil {
		return err
	}
	info, err := AssetInfo(name)
	if err != nil {
		return err
	}
	err = os.MkdirAll(_filePath(dir, filepath.Dir(name)), os.FileMode(0755))
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(_filePath(dir, name), data, info.Mode())
	if err != nil {
		return err
	}
	err = os.Chtimes(_filePath(dir, name), info.ModTime(), info.ModTime())
	if err != nil {
		return err
	}
	return nil
}

// RestoreAssets restores an asset under the given directory recursively
func RestoreAssets(dir, name string) error {
	children, err := AssetDir(name)
	// File
	if err != nil {
		return RestoreAsset(dir, name)
	}
	// Dir
	for _, child := range children {
		err = RestoreAssets(dir, filepath.Join(name, child))
		if err != nil {
			return err
		}
	}
	return nil
}

func _filePath(dir, name string) string {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	return filepath.Join(append([]string{dir}, strings.Split(cannonicalName, "/")...)...)
}
