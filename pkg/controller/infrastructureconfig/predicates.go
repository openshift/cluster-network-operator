package infrastructureconfig

import (
	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// onPremPlatformPredicate implements a create and update predicate function on on-prem platform changes
func onPremPlatformPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			infra := e.Object.(*configv1.Infrastructure)
			return isOnPremPlatform(infra)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			infra := e.ObjectNew.(*configv1.Infrastructure)
			return isOnPremPlatform(infra)
		},
	}
}

func isOnPremPlatform(infra *configv1.Infrastructure) bool {
	switch infra.Spec.PlatformSpec.Type {
	case configv1.BareMetalPlatformType:
		fallthrough
	case configv1.VSpherePlatformType:
		fallthrough
	case configv1.OpenStackPlatformType:
		fallthrough
	case configv1.OvirtPlatformType:
		fallthrough
	case configv1.NutanixPlatformType:
		return true
	default:
		return false
	}
}
