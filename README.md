# Openshift Network Operator

This is an operator that manages the networking components for an openshift cluster.


## Building

You can build an image using buildah with

```
./hack/build-image.sh
```

You might need sudo:
```
BUILDCMD="sudo buildah bud" ./hack/build-image.sh
```

Or you could use a docker that supports multi-stage builds
```
BUILDCMD="docker build" ./hack/build-image.sh
```

## Running

There are some premade Kubernetes manifests to run a development build. After setting the image URL to something sane in the daemonset, do:

```
kubectl create -f ./deploy/devel
```

Then you can watch the daemonset with `kubectl -n openshift-network-operator logs <PODID>`.
