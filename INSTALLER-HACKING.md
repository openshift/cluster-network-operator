# Running local builds with the installer

If you want to run a local build with the installer, you need to do a bit of hacking. There is a script that will do most of the dirty work for you. It will modify the installer so the network operator doesn't run, and spin up a local copy of the network operator.

This requires `oc` and `kubectl` in your $PATH.

## Steps

You will need to execute all steps for every installation, since the installer deletes the intermediate files we customize.

1. In one window, prepare the cluster:

```
export CLUSTER_DIR=<your cluster directory>
openshift-install --dir=$CLUSTER_DIR create manifests
```

2. In another window, prepare the operator. This will apply some overrides that disable the default cluster-network-operator. It will also extract the release image.

```
export CLUSTER_DIR=<your cluster directory>
hack/run-locally.sh prepare
hack/build-go.sh
```

3. Optionally, override image references. If you are doing downstream image development, e.g. working on openshift-sdn, you should build, publish, and edit `$CLUSTER_DIR/env.sh` to point to your development image.

4. In the installer window, create the cluster *and move on to the next step*

```
openshift-install --dir=$CLUSTER_DIR create cluster
```

5. In the operator window, *while the installer is running*, launch the operator locally
```
hack/run-locally.sh start
```


## Tips, Tricks, & Limitations

### Cleaning up
After destroying your cluster, you should delete your `$CLUSTER_DIR`.

If you're using libvirt, you can use `./scripts/maintenance/virsh-cleanup.sh` in the installer repository to quickly delete the virtual machines instead of waiting for Terraform.

### RBAC
This runs the operator with the `admin` role, so it will have full permissions. When run by the real installer, this will not be the case. Created objects, e.g. DaemonSets, will still have RBAC, though.

### Saving install-config between runs
If you're sick of answering all the questions over and over, you can shortcut the installer. Just do
```
openshift-install --dir=$CLUSTER_DIR create install-config
cp $CLUSTER_DIR/install-config.yaml ~/.cache/openshift-install/install-config.yaml
```

Then, for subsequent installer runs, you can do
```
rm -r $CLUSTER_DIR; mkdir -p $CLUSTER_DIR
cp ~/.cache/openshift-install/install-config.yaml $CLUSTER_DIR/
```
