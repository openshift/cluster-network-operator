package main

import (
	"context"
	"log"
	"runtime"
	"time"

	netop "github.com/openshift/cluster-network-operator/pkg/operator"
	"github.com/operator-framework/operator-sdk/pkg/leader"
	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()

	var manifestPath string
	pflag.StringVar(&manifestPath, "bindata", "./bindata", "directory containing network manifests")

	err := leader.Become(context.Background(), "cluster-network-operator")
	if err != nil {
		log.Fatalf("Failed to become leader: %v", err)
		return
	}

	//sdk.ExposeMetricsPort()

	resource := "networkoperator.openshift.io/v1"
	kind := "NetworkConfig"
	namespace := "" //non namespaced

	resyncPeriod := time.Duration(60) * time.Second
	logrus.Infof("Watching %s, %s, %s, %d", resource, kind, namespace, resyncPeriod)
	sdk.Watch(resource, kind, namespace, resyncPeriod)
	sdk.Handle(netop.MakeHandler(manifestPath))
	sdk.Run(context.Background())
}
