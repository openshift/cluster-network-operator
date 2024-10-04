package genericoperatorclient

import (
	"net/http"

	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
)

func NewOperatorClientWithClient(clock clock.PassiveClock, httpClient *http.Client, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, extractApplySpec OperatorSpecExtractorFunc, extractApplyStatus OperatorStatusExtractorFunc) (v1helpers.OperatorClientWithFinalizers, dynamicinformer.DynamicSharedInformerFactory, error) {
	return NewOperatorClientWithConfigNameWithClient(clock, httpClient, gvr, gvk, defaultConfigName, extractApplySpec, extractApplyStatus)

}

func NewOperatorClientWithConfigNameWithClient(clock clock.PassiveClock, httpClient *http.Client, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, configName string, extractApplySpec OperatorSpecExtractorFunc, extractApplyStatus OperatorStatusExtractorFunc) (v1helpers.OperatorClientWithFinalizers, dynamicinformer.DynamicSharedInformerFactory, error) {
	dynamicClient, err := dynamic.NewForConfigAndClient(&rest.Config{}, httpClient)
	if err != nil {
		return nil, nil, err
	}

	return newClusterScopedOperatorClient(clock, dynamicClient, gvr, gvk, configName,
		convertOperatorSpecToStaticPodOperatorSpec(extractApplySpec), convertOperatorStatusToStaticPodOperatorStatus(extractApplyStatus))
}

func NewStaticPodOperatorClientWithConfigNameWithClient(clock clock.PassiveClock, httpClient *http.Client, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, configName string, extractApplySpec StaticPodOperatorSpecExtractorFunc, extractApplyStatus StaticPodOperatorStatusExtractorFunc) (v1helpers.StaticPodOperatorClient, dynamicinformer.DynamicSharedInformerFactory, error) {
	dynamicClient, err := dynamic.NewForConfigAndClient(&rest.Config{}, httpClient)
	if err != nil {
		return nil, nil, err
	}

	return newClusterScopedOperatorClient(clock, dynamicClient, gvr, gvk, configName,
		extractApplySpec, extractApplyStatus)
}
