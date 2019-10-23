package operconfig

import (
	"bytes"
	"context"
	"log"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var _ reconcile.Reconciler = &ReconcileConfigMaps{}

// ReconcileConfigMaps watches for updates to specified resources
type ReconcileConfigMaps struct {
	client client.Client
}

// newConfigMapReconciler returns a new reconcile.Reconciler
func newConfigMapReconciler(client client.Client) reconcile.Reconciler {
	return &ReconcileConfigMaps{client: client}
}

// Add adds  when the Manager is Started.
func AddConfigMapReconciler(mgr manager.Manager, status *statusmanager.StatusManager) error {
	return addConfigMapReconciler(mgr, newConfigMapReconciler(mgr.GetClient()))
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func addConfigMapReconciler(mgr manager.Manager, r reconcile.Reconciler) error {
	// ConfigMap reconciler
	c, err := controller.New("configmap-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForOwner{OwnerType: &operv1.Network{}, IsController: true})
	if err != nil {
		return err
	}

	return nil
}

func (r *ReconcileConfigMaps) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	if request.Namespace == "" && request.Name == names.OPERATOR_CONFIG {
		return r.ReconcileMultusWebhook(request)
	}
	return reconcile.Result{}, nil
}

// ReconcileMultusWebhook updates ValidatingWebhookConfiguration CABundle, given from SERVICE_CA_CONFIGMAP
func (r *ReconcileConfigMaps) ReconcileMultusWebhook(request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling update for %s from %s/%s\n", names.SERVICE_CA_CONFIGMAP, request.Namespace, request.Name)

	caBundleConfigMap := &corev1.ConfigMap{}
	configMapName := types.NamespacedName{
		Namespace: names.APPLIED_NAMESPACE,
		Name:      names.SERVICE_CA_CONFIGMAP,
	}
	err := r.client.Get(context.TODO(), configMapName, caBundleConfigMap)
	if err != nil {
		log.Println(err)
		return reconcile.Result{}, err
	}

	caBundleData, ok := caBundleConfigMap.Data["service-ca.crt"]
	if !ok {
		// Need to wait to fill the service-ca.crt by ca-operator.
		return reconcile.Result{}, err
	}

	webhookConfig := &admissionregistrationv1beta1.ValidatingWebhookConfiguration{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: names.MULTUS_VALIDATING_WEBHOOK}, webhookConfig)
	if errors.IsNotFound(err) {
		log.Println("Webhook is not found.")
		return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
	}

	webhookConfig = webhookConfig.DeepCopy()
	modified := false
	for idx, webhook := range webhookConfig.Webhooks {
		// Update CABundle if CABundle is empty or updated.
		if webhook.ClientConfig.CABundle == nil || !bytes.Equal(webhook.ClientConfig.CABundle, []byte(caBundleData)) {
			modified = true
			webhookConfig.Webhooks[idx].ClientConfig.CABundle = []byte(caBundleData)
		}
	}
	if !modified {
		return reconcile.Result{}, err
	}

	// Update webhookConfig
	err = r.client.Update(context.TODO(), webhookConfig)
	if err != nil {
		log.Println(err)
	}

	return reconcile.Result{}, nil
}
