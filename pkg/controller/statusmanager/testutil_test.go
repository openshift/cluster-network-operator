package statusmanager

import (
	"context"

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
