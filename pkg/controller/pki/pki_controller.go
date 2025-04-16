package pki

// pki implements a simple PKI controller that creates a CA and certificates
// for that CA.
// TODO:
//   - Add the ability in library-go to set our OwnerReference
//   - Find a way to set RelatedObjects

import (
	"context"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"time"

	netopv1 "github.com/openshift/cluster-network-operator/pkg/apis/network/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/eventrecorder"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/library-go/pkg/operator/certrotation"
	"github.com/pkg/errors"

	features "github.com/openshift/api/features"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	OneYear = 365 * 24 * time.Hour
)

// Add attaches our control loop to the manager and watches for PKI objects
func Add(mgr manager.Manager, status *statusmanager.StatusManager, client cnoclient.Client) error {
	r, err := newPKIReconciler(mgr, status, client)
	if err != nil {
		return err
	}

	// Create a new controller
	c, err := controller.New("pki-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource PKI.network.operator.openshift.io/v1
	err = c.Watch(source.Kind[crclient.Object](mgr.GetCache(), &netopv1.OperatorPKI{}, &handler.EnqueueRequestForObject{}))
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &PKIReconciler{}

// PKIReconciler manages the creation of one or more key + CA bundles
type PKIReconciler struct {
	mgr       manager.Manager
	clientset *kubernetes.Clientset
	status    *statusmanager.StatusManager

	// one PKI per CA
	pkis map[types.NamespacedName]*pki
	// For computing status
	pkiErrs map[types.NamespacedName]error

	certDuration time.Duration
}

// The periodic resync interval.
// We will re-run the reconciliation logic, even if the configuration
// hasn't changed.
var ResyncPeriod = 5 * time.Minute

// newPKIReconciler creates the toplevel reconciler that receives PKI updates
// and configures the CertRotationController accordingly
func newPKIReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) (reconcile.Reconciler, error) {
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	kubeConfig := c.Default().Config()
	kubeClient := c.Default().Kubernetes()
	configClient, err := configclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, err
	}
	configInformers := configinformers.NewSharedInformerFactory(configClient, 10*time.
		Minute)

	desiredVersion := os.Getenv("RELEASE_VERSION")
	missingVersion := "0.0.1-snapshot"

	eventRecorder := events.NewKubeRecorder(kubeClient.CoreV1().Events("openshift-network-operator"), "cluster-network-operator", &corev1.ObjectReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  "openshift-network-operator",
		Name:       "network-operator",
	}, clock.RealClock{})

	// By default, this will exit(0) the process if the featuregates ever change to a different set of values.
	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion, missingVersion,
		configInformers.Config().V1().ClusterVersions(), configInformers.Config().
			V1().FeatureGates(),
		eventRecorder,
	)
	// TODO: 1) If other controllers in CNO also want to use featureGates then we should move this code to outside
	// operconfig-controller 2) For now we pass the neverStop channel; FIXME: use c.Default().AddCustomInformer and
	// change this to pass a proper stop channel and context which are closed and cancelled properly upon exit.
	go featureGateAccessor.Run(context.TODO())
	go configInformers.Start(wait.NeverStop)

	klog.Infof("Waiting for feature gates initialization...")
	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
		featureGates, err := featureGateAccessor.CurrentFeatureGates()
		if err != nil {
			return nil, err
		} else {
			klog.Infof("FeatureGates initialized: knownFeatureGates=%v", featureGates.KnownFeatures())
		}
	case <-time.After(1 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for FeatureGate detection")
	}

	featureGates, err := featureGateAccessor.CurrentFeatureGates()
	if err != nil {
		return nil, err
	}

	certDuration := 365 * 24 * time.Hour / 2
	if featureGates.Enabled(features.FeatureShortCertRotation) {
		certDuration = time.Hour
	}

	return &PKIReconciler{
		mgr:       mgr,
		status:    status,
		clientset: clientset,

		pkis:         map[types.NamespacedName]*pki{},
		pkiErrs:      map[types.NamespacedName]error{},
		certDuration: certDuration,
	}, nil
}

// Reconcile configures a CertRotationController from a PKI object
func (r *PKIReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)
	log.Printf("Reconciling pki.network.operator.openshift.io %s\n", request.NamespacedName)

	obj := &netopv1.OperatorPKI{}
	err := r.mgr.GetClient().Get(ctx, request.NamespacedName, obj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("PKI %s seems to have been deleted\n", request.NamespacedName)
			return reconcile.Result{}, nil
		}
		log.Println(err)
		return reconcile.Result{}, err
	}

	// Check to see if we already know this object
	existing := r.pkis[request.NamespacedName]
	if existing != nil {
		// If the spec has changed, refresh
		if !reflect.DeepEqual(obj.Spec, existing.spec) {
			log.Printf("PKI %s has changed, refreshing\n", request.NamespacedName)
			delete(r.pkis, request.NamespacedName)
			existing = nil
		}
	}
	if existing == nil {
		existing, err = newPKI(obj, r.clientset, r.mgr, r.certDuration)
		if err != nil {
			log.Println(err)
			r.pkiErrs[request.NamespacedName] =
				errors.Wrapf(err, "could not parse PKI.Spec %s", request.NamespacedName)
			r.setStatus()
			return reconcile.Result{}, err
		}
		r.pkis[request.NamespacedName] = existing
	}

	err = existing.sync()
	if err != nil {
		log.Println(err)
		r.pkiErrs[request.NamespacedName] =
			errors.Wrapf(err, "could not reconcile PKI %s", request.NamespacedName)
		r.setStatus()
		return reconcile.Result{}, err
	}

	log.Println("successful reconciliation")
	delete(r.pkiErrs, request.NamespacedName)
	r.setStatus()
	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

// setStatus summarizes the status of all PKI objects and updates the statusmanager
// as appropriate.
func (r *PKIReconciler) setStatus() {
	if len(r.pkiErrs) == 0 {
		r.status.SetNotDegraded(statusmanager.PKIConfig)
	} else {
		msgs := []string{}
		for _, e := range r.pkiErrs {
			msgs = append(msgs, e.Error())
		}
		r.status.SetDegraded(statusmanager.PKIConfig, "PKIError", strings.Join(msgs, ", "))
	}
}

// pki is the internal type that represents a single PKI CRD. It manages the
// business of reconciling the certificate objects
type pki struct {
	spec       netopv1.OperatorPKISpec
	controller factory.Controller
}

// newPKI creates a CertRotationController for the supplied configuration
func newPKI(config *netopv1.OperatorPKI, clientset *kubernetes.Clientset, mgr manager.Manager, certDuration time.Duration) (*pki, error) {
	spec := config.Spec

	// Ugly: the existing cache + informers used as part of the controller-manager
	// can't be used, because they're untyped. So, we need to create our own.
	// However, this has a few advantages - namely, we're creating a namespace-scoped
	// watch, which is much more efficient than watching all Secrets and ConfigMaps
	// TODO: consider adding a label selector to the watch, since we can do that.

	inf := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		24*time.Hour,
		informers.WithNamespace(config.Namespace))

	cont := certrotation.NewCertRotationController(
		fmt.Sprintf("%s/%s", config.Namespace, config.Name), // name, not really used
		certrotation.RotatedSigningCASecret{
			Namespace: config.Namespace,
			Name:      config.Name + "-ca",
			AdditionalAnnotations: certrotation.AdditionalAnnotations{
				JiraComponent: names.ClusterNetworkOperatorJiraComponent,
			},
			Validity:      10 * OneYear,
			Refresh:       9 * OneYear,
			Informer:      inf.Core().V1().Secrets(),
			Lister:        inf.Core().V1().Secrets().Lister(),
			Client:        clientset.CoreV1(),
			EventRecorder: &eventrecorder.LoggingRecorder{},
		},
		certrotation.CABundleConfigMap{
			Namespace: config.Namespace,
			Name:      config.Name + "-ca",
			AdditionalAnnotations: certrotation.AdditionalAnnotations{
				JiraComponent: names.ClusterNetworkOperatorJiraComponent,
			},
			Lister:        inf.Core().V1().ConfigMaps().Lister(),
			Informer:      inf.Core().V1().ConfigMaps(),
			Client:        clientset.CoreV1(),
			EventRecorder: &eventrecorder.LoggingRecorder{},
		},
		certrotation.RotatedSelfSignedCertKeySecret{
			Namespace: config.Namespace,
			Name:      config.Name + "-cert",
			AdditionalAnnotations: certrotation.AdditionalAnnotations{
				JiraComponent: names.ClusterNetworkOperatorJiraComponent,
			},
			Validity: certDuration,
			Refresh:  certDuration / 2,
			CertCreator: &certrotation.ServingRotation{
				Hostnames: func() []string { return []string{spec.TargetCert.CommonName} },

				// Force the certificate to also be client
				CertificateExtensionFn: []crypto.CertificateExtensionFunc{
					toClientCert,
				},
			},
			Lister:        inf.Core().V1().Secrets().Lister(),
			Informer:      inf.Core().V1().Secrets(),
			Client:        clientset.CoreV1(),
			EventRecorder: &eventrecorder.LoggingRecorder{},
		},
		&eventrecorder.LoggingRecorder{},
		nil,
	)

	out := &pki{
		controller: cont,
	}
	config.Spec.DeepCopyInto(&out.spec)

	ch := make(chan struct{})
	inf.Start(ch)
	inf.WaitForCacheSync(ch)

	return out, nil
}

// sync causes the underlying cert controller to try and reconcile
func (p *pki) sync() error {
	runOnceCtx := context.WithValue(context.Background(), certrotation.RunOnceContextKey, true) //nolint:staticcheck
	return p.controller.Sync(runOnceCtx, nil)
}

// toClientCert is a certificate "decorator" that adds ClientAuth to the
// list of ExtendedKeyUsages. This allows the generated certificate to be
// used for both client and server auth.
func toClientCert(cert *x509.Certificate) error {
	if len(cert.ExtKeyUsage) == 0 {
		return nil
	}

	found := false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			found = true
			break
		}
	}

	if !found {
		cert.ExtKeyUsage = append(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	}
	return nil
}
