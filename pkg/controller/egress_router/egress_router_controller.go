package egress_router

// egress router implements a controller for the egress router CNI plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"

	"path/filepath"
	"reflect"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	netopv1 "github.com/openshift/api/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Attach control loop to the manager and watch for Egress Router objects
func Add(mgr manager.Manager, status *statusmanager.StatusManager, cli *cnoclient.Client) error {
	r, err := newEgressRouterReconciler(mgr, status, cli)
	if err != nil {
		return err
	}

	// Create a new controller
	c, err := controller.New("egress-router-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource EgressRouter.network.operator.openshift.io/v1
	err = c.Watch(&source.Kind{Type: &netopv1.EgressRouter{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &EgressRouterReconciler{}
var manifestDir = "bindata/"

type egressrouter struct {
	spec netopv1.EgressRouterSpec
}

type EgressRouterReconciler struct {
	mgr    manager.Manager
	client *cnoclient.Client
	status *statusmanager.StatusManager

	egressrouters    map[types.NamespacedName]*egressrouter
	egressrouterErrs map[types.NamespacedName]error
}

var ResyncPeriod = 5 * time.Minute

func newEgressRouterReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c *cnoclient.Client) (reconcile.Reconciler, error) {

	return &EgressRouterReconciler{
		mgr:    mgr,
		status: status,
		client: c,

		egressrouters:    map[types.NamespacedName]*egressrouter{},
		egressrouterErrs: map[types.NamespacedName]error{},
	}, nil
}

func (r EgressRouterReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconciling egressrouter.network.operator.openshift.io %s\n", request.NamespacedName)

	obj := &netopv1.EgressRouter{}
	err := r.mgr.GetClient().Get(ctx, request.NamespacedName, obj)

	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("Egress Router %s seems to have been deleted\n", request.NamespacedName)
			return reconcile.Result{}, nil
		}
		klog.Error(err)
		return reconcile.Result{}, err
	}

	// Check to see if we already know this object
	existing := r.egressrouters[request.NamespacedName]
	if existing != nil {
		if !reflect.DeepEqual(obj.Spec, existing.spec) {
			klog.Infof("Egress Router %s has changed, refreshing\n", request.NamespacedName)
			delete(r.egressrouters, request.NamespacedName)
			existing = nil
		}
	}

	if existing == nil {
		klog.Infof("Creating a new Egress Router")
		// Set owner reference to the controller
		boolTrue := bool(true)
		EgressRouterOwnerReferences := []metav1.OwnerReference{
			{
				APIVersion: "network.operator.openshift.io/v1",
				Kind:       "EgressRouter",
				Name:       obj.Name,
				UID:        obj.UID,
				Controller: &boolTrue,
			},
		}
		err := r.ensureEgressRouter(ctx, manifestDir, request.Namespace, obj, EgressRouterOwnerReferences)

		if err != nil {
			klog.Error(err)
			r.egressrouterErrs[request.NamespacedName] =
				errors.Wrapf(err, "could not reconcile Egress Router %s", request.NamespacedName)
			r.setStatus()
			return reconcile.Result{}, err
		}

		r.egressrouters[request.NamespacedName] = existing
	}

	if err != nil {
		klog.Error(err)
		r.egressrouterErrs[request.NamespacedName] =
			errors.Wrapf(err, "could not reconcile Egress Router %s", request.NamespacedName)
		r.setStatus()
		return reconcile.Result{}, err
	}

	klog.Infof("successful reconciliation")
	delete(r.egressrouterErrs, request.NamespacedName)
	r.setStatus()
	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

// setStatus summarizes the status of all Egress Router objects and updates the statusmanager
// as appropriate.
func (r *EgressRouterReconciler) setStatus() {
	if len(r.egressrouterErrs) == 0 {
		r.status.SetNotDegraded(statusmanager.EgressRouterConfig)
	} else {
		msgs := []string{}
		for _, e := range r.egressrouterErrs {
			msgs = append(msgs, e.Error())
		}
		r.status.SetDegraded(statusmanager.EgressRouterConfig, "EgressRouterError", strings.Join(msgs, ", "))
	}
}

// getAllowedDestinationsConfigJSONi generates AllowedDestinations json config
// order of the fields need to match egress-route-cni macvlan module
func getAllowedDestinationsConfigJSON(RedirectRules []netopv1.L4RedirectRule) (string, error) {
	config := make([]string, len(RedirectRules))

	for idx, rule := range RedirectRules {
		if rule.Port != 0 && len(rule.Protocol) != 0 {
			if rule.TargetPort != 0 {
				config[idx] = fmt.Sprintf("%d %s %s %d", rule.Port, rule.Protocol, rule.DestinationIP, rule.TargetPort)
			} else {
				config[idx] = fmt.Sprintf("%d %s %s", rule.Port, rule.Protocol, rule.DestinationIP)
			}
		} else {
			config[idx] = rule.DestinationIP
		}
	}

	jsonByte, err := json.Marshal(config)
	if err != nil {
		return "", nil
	}

	return string(jsonByte), nil
}

func (r *EgressRouterReconciler) ensureEgressRouter(ctx context.Context, manifestDir string, namespace string, router *netopv1.EgressRouter, EgressRouterOwnerReferences []metav1.OwnerReference) error {
	var err error
	if len(router.Spec.Addresses) == 0 {
		return fmt.Errorf("Error: router without addresses")
	}
	out := []*uns.Unstructured{}
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["EgressRouterNamespace"] = namespace
	if isItValidCidr(string(router.Spec.Addresses[0].IP)) {
		data.Data["Addresses"] = router.Spec.Addresses[0].IP
	}
	if isItValidIPAddress(router.Spec.Addresses[0].Gateway) {
		data.Data["Gateway"] = router.Spec.Addresses[0].Gateway
	}
	data.Data["AllowedDestinations"], err = getAllowedDestinationsConfigJSON(router.Spec.Redirect.RedirectRules)
	if err != nil {
		return errors.Wrap(err, "failed to render AllowedDestinations config")
	}
	data.Data["FallbackIP"] = router.Spec.Redirect.FallbackIP
	data.Data["mode"] = router.Spec.Mode
	data.Data["network_interfaces"] = router.Spec.NetworkInterface
	data.Data["EgressRouterPodImage"] = os.Getenv("EGRESS_ROUTER_CNI_IMAGE")
	manifests, err := render.RenderDir(filepath.Join(manifestDir, "egress-router"), &data)
	if err != nil {
		return err
	}
	out = append(out, manifests...)

	for _, obj := range out {
		klog.Infof("Assigning owner references")
		obj.SetOwnerReferences(EgressRouterOwnerReferences)
		klog.Infof("Applying manifest")
		if err := apply.ApplyObject(ctx, r.client, obj, "egress_router"); err != nil {
			klog.Infof("could not apply egress router object: %v", err)
			return err
		}
	}

	return nil
}

func isItValidCidr(cidr string) bool {
	_, _, err := net.ParseCIDR(cidr)
	if err != nil {
		klog.Error(err)
		return false
	}
	return true
}

func isItValidIPAddress(ip string) bool {
	return net.ParseIP(ip) != nil
}
