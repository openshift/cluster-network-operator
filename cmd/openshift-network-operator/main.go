package main

import (
	"context"
	"runtime"
	"time"

	netop "github.com/openshift/openshift-network-operator/pkg/operator"
	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	"github.com/sirupsen/logrus"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()

	//sdk.ExposeMetricsPort()

	resource := "networkoperator.openshift.io/v1"
	kind := "NetworkConfig"
	namespace := "" //non namespaced

	resyncPeriod := time.Duration(60) * time.Second
	logrus.Infof("Watching %s, %s, %s, %d", resource, kind, namespace, resyncPeriod)
	sdk.Watch(resource, kind, namespace, resyncPeriod)
	sdk.Handle(netop.MakeHandler("./manifests"))
	sdk.Run(context.TODO())
}
