package operator

import (
	"context"
	"sync"

	"github.com/openshift/openshift-network-operator/pkg/apis/networkoperator/v1"
	"github.com/operator-framework/operator-sdk/pkg/sdk"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sirupsen/logrus"
)

func MakeHandler(manifestDir string) *Handler {
	return &Handler{ManifestDir: manifestDir}
}

type Handler struct {
	// Lock held when rendering and reconciling
	syncLock sync.Mutex

	config      *v1.NetworkConfig
	ManifestDir string
}

func (h *Handler) SetConfig(conf *v1.NetworkConfig) {
	h.syncLock.Lock()
	defer h.syncLock.Unlock()
	h.config = conf
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	switch o := event.Object.(type) {
	case *v1.NetworkConfig:
		logrus.Info("Got new network configuration")
		h.SetConfig(o)

		h.Sync(ctx)
	}
	return nil
}

// Reconcile renders all desired API objects, then reconciles them against
// the existing ones in the API server
func (h *Handler) Sync(ctx context.Context) error {
	if h.config == nil {
		logrus.Info("Sync() but no configuration yet")
		return nil
	}

	h.syncLock.Lock()
	defer h.syncLock.Unlock()

	objs, err := h.Render()
	if err != nil {
		logrus.Errorf("render error: %s", err)
		// TODO: error propagation
		return err
	}

	return h.Reconcile(ctx, objs)
}

func (h *Handler) Render() ([]*uns.Unstructured, error) {
	logrus.Info("Starting render phase")
	objs := []*uns.Unstructured{}

	// render default network
	o, err := h.RenderDefaultNetwork()
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render kube-proxy
	// TODO: kube-proxy

	// render additional networks
	// TODO: extra networks

	logrus.Infof("Render phase done, rendered %d objects", len(objs))
	return objs, nil
}

func (h *Handler) Reconcile(ctx context.Context, objs []*uns.Unstructured) error {
	logrus.Infof("Starting reconcile phase")
	for _, obj := range objs {
		err := h.ReconcileObject(ctx, obj)
		if err != nil {
			logrus.Error(err)
		}
		// TODO: report result of object in status (error or success)
	}
	// TODO: prune unneeded objects
	logrus.Infof("reconcile phase done")
	return nil
}
