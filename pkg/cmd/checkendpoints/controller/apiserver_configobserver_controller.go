package controller

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	configinformers "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlisters "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/configobserver"
	apiserver "github.com/openshift/library-go/pkg/operator/configobserver/apiserver"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/client-go/tools/cache"
)

// APIServerConfigObserverController observes the APIServer CR and extracts
// TLS security profile configuration for use by the network check source.
type APIServerConfigObserverController struct {
	factory.Controller
	apiServerLister configlisters.APIServerLister
}

// Listers implements configobserver.Listers and apiserver.APIServerLister
// to provide access to the resource syncer and APIServer lister
type Listers struct {
	apiServerLister configlisters.APIServerLister
	resourceSyncer  resourcesynccontroller.ResourceSyncer
}

func (l Listers) APIServerLister() configlisters.APIServerLister {
	return l.apiServerLister
}

func (l Listers) ResourceSyncer() resourcesynccontroller.ResourceSyncer {
	return l.resourceSyncer
}

func (l Listers) PreRunHasSynced() []cache.InformerSynced {
	return []cache.InformerSynced{}
}

// NewAPIServerConfigObserverController creates a new controller that observes
// the APIServer CR and updates the observed configuration based on the TLS
// security profile settings.
func NewAPIServerConfigObserverController(
	operatorClient v1helpers.OperatorClient,
	apiServerInformer configinformers.APIServerInformer,
	resourceSyncer resourcesynccontroller.ResourceSyncer,
	eventRecorder events.Recorder,
) (*APIServerConfigObserverController, error) {
	// Define the paths where TLS configuration should be stored in the operator config
	minTLSVersionPath := []string{"tlsSecurityProfile", "minTLSVersion"}
	cipherSuitesPath := []string{"tlsSecurityProfile", "cipherSuites"}

	// Create listers for the config observer
	listers := Listers{
		apiServerLister: apiServerInformer.Lister(),
		resourceSyncer:  resourceSyncer,
	}

	// Create the config observer with the TLS security profile observer function
	// This returns a factory.Controller that handles syncing automatically
	controller := configobserver.NewConfigObserver(
		"APIServerConfigObserver",
		operatorClient,
		eventRecorder,
		listers,
		[]factory.Informer{apiServerInformer.Informer()},
		func(listers configobserver.Listers, recorder events.Recorder, existingConfig map[string]interface{}) (map[string]interface{}, []error) {
			return apiserver.ObserveTLSSecurityProfileWithPaths(listers, recorder, existingConfig, minTLSVersionPath, cipherSuitesPath)
		},
	)

	return &APIServerConfigObserverController{
		Controller:      controller,
		apiServerLister: apiServerInformer.Lister(),
	}, nil
}

// GetObservedTLSSecurityProfile retrieves the currently observed TLS security profile
// from the APIServer CR. This can be used by other components that need access to
// the TLS configuration.
func (c *APIServerConfigObserverController) GetObservedTLSSecurityProfile() (*configv1.TLSSecurityProfile, error) {
	apiServer, err := c.apiServerLister.Get("cluster")
	if err != nil {
		return nil, fmt.Errorf("failed to get APIServer 'cluster': %w", err)
	}
	return apiServer.Spec.TLSSecurityProfile, nil
}
