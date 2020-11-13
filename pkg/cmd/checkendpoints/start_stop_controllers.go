package checkendpoints

import (
	"context"
	"os"
	"sync"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	apiextensionsinformersv1 "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	apiextensionslistersv1 "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
)

// timeToStartController will either close its ready chan, when the podnetworkconnectivitychecks
// type exists, or send an error to the ready chan if an error has occurred.
type timeToStartController struct {
	factory.Controller
	crdLister apiextensionslistersv1.CustomResourceDefinitionLister
	ready     chan error
}

func newTimeToStartController(crdInformer apiextensionsinformersv1.CustomResourceDefinitionInformer, recorder events.Recorder) *timeToStartController {
	c := &timeToStartController{
		crdLister: crdInformer.Lister(),
		ready:     make(chan error, 1),
	}
	var once sync.Once
	c.Controller = factory.New().
		WithInformers(crdInformer.Informer()).
		WithSync(func(context.Context, factory.SyncContext) error {
			ok, err := podNetworkConnectivityCheckTypeExists(c.crdLister)
			if err != nil {
				c.ready <- err
				return err
			}
			if ok {
				once.Do(func() { close(c.ready) })
			}
			return nil
		}).
		ToController("CheckEndpointsTimeToStart", recorder)
	return c
}

func (c *timeToStartController) Ready() <-chan error {
	return c.ready
}

// stopController stops the process using os.Exit(0) if the podnetworkconnectivitychecks crd
// stops being available after the process has started.
type stopController struct {
	factory.Controller
	crdLister apiextensionslistersv1.CustomResourceDefinitionLister
}

func newStopController(crdInformer apiextensionsinformersv1.CustomResourceDefinitionInformer, recorder events.Recorder) *stopController {
	c := &stopController{
		crdLister: crdInformer.Lister(),
	}
	c.Controller = factory.New().
		WithInformers(crdInformer.Informer()).
		WithSync(func(context.Context, factory.SyncContext) error {
			ok, err := podNetworkConnectivityCheckTypeExists(c.crdLister)
			if err != nil {
				return err
			}
			if !ok {
				klog.Info("The server doesn't have a resource type \"podnetworkconnectivitychecks.controlplane.operator.openshift.io\".")
				os.Exit(0)
			}
			return nil
		}).
		ToController("CheckEndpointsStop", recorder)
	return c
}

func podNetworkConnectivityCheckTypeExists(lister apiextensionslistersv1.CustomResourceDefinitionLister) (bool, error) {
	_, err := lister.Get("podnetworkconnectivitychecks.controlplane.operator.openshift.io")
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
