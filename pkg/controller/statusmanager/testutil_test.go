package statusmanager

import (
	"context"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func setFakeListers(sm *StatusManager) {
	sm.dsListers = map[string]DaemonSetLister{
		"": &fakeDaemonSetLister{sm},
	}
	sm.depListers = map[string]DeploymentLister{
		"": &fakeDeploymentLister{sm},
	}
	sm.ssListers = map[string]StatefulSetLister{
		"": &fakeStatefulSetLister{sm},
	}
	sm.mcLister = &fakeMachineConfigLister{sm}
	sm.mcpLister = &fakeMachineConfigPoolLister{sm}
}

type fakeDaemonSetLister struct {
	sm *StatusManager
}

type fakeDeploymentLister struct {
	sm *StatusManager
}
type fakeStatefulSetLister struct {
	sm *StatusManager
}
type fakeMachineConfigLister struct {
	sm *StatusManager
}

type fakeMachineConfigPoolLister struct {
	sm *StatusManager
}

func (f *fakeDaemonSetLister) List(selector labels.Selector) (ret []*appsv1.DaemonSet, err error) {
	l := &appsv1.DaemonSetList{}
	err = f.sm.client.Default().CRClient().List(context.TODO(), l, &crclient.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, err
	}
	for i := range l.Items {
		ret = append(ret, &l.Items[i])
	}
	return ret, nil
}

func (f *fakeDeploymentLister) List(selector labels.Selector) (ret []*appsv1.Deployment, err error) {
	l := &appsv1.DeploymentList{}
	err = f.sm.client.Default().CRClient().List(context.TODO(), l, &crclient.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, err
	}
	for i := range l.Items {
		ret = append(ret, &l.Items[i])
	}
	return ret, nil
}

func (f *fakeStatefulSetLister) List(selector labels.Selector) (ret []*appsv1.StatefulSet, err error) {
	l := &appsv1.StatefulSetList{}
	err = f.sm.client.Default().CRClient().List(context.TODO(), l, &crclient.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, err
	}
	for i := range l.Items {
		ret = append(ret, &l.Items[i])
	}
	return ret, nil
}

func (f *fakeMachineConfigLister) List(selector labels.Selector) (ret []*mcfgv1.MachineConfig, err error) {
	machineConfigs := &mcfgv1.MachineConfigList{}
	err = f.sm.client.Default().CRClient().List(context.TODO(), machineConfigs, &crclient.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, err
	}
	for i := range machineConfigs.Items {
		ret = append(ret, &machineConfigs.Items[i])
	}
	return ret, nil
}

func (f *fakeMachineConfigPoolLister) List(selector labels.Selector) (ret []*mcfgv1.MachineConfigPool, err error) {
	pools := &mcfgv1.MachineConfigPoolList{}
	err = f.sm.client.Default().CRClient().List(context.TODO(), pools, &crclient.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, err
	}
	for i := range pools.Items {
		ret = append(ret, &pools.Items[i])
	}
	return ret, nil
}
