# OVN-Kubernetes Feature Disabling

This document describes how to disable specific OVN-Kubernetes networking features through the Network operator API.

## Overview

OVN-Kubernetes provides various networking features that are enabled by default. In some specialized deployments (such as DPU host environments), certain features may not be supported or desired. The Network operator provides API fields to explicitly disable these features cluster-wide.

## Disabling Features via API

The following features can be disabled via the `Network.operator.openshift.io` API in the `spec.defaultNetwork.ovnKubernetesConfig` section:

### Egress Features

Set `disableEgressFeatures: true` to disable all egress-related features:
- Egress IP
- Egress Firewall
- Egress QoS
- Egress Service

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      disableEgressFeatures: true
```

### Multicast Support

Set `disableMulticast: true` to disable OVN multicast support:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      disableMulticast: true
```

### Multi-External Gateway

Set `disableMultiExternalGateway: true` to disable multi-external gateway support:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      disableMultiExternalGateway: true
```

## Other Feature Controls

### Multi-Network Support

Multi-network support is controlled at the top level of the Network spec:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  disableMultiNetwork: true
```

### Multi-Network Policy

Multi-network policy is controlled via the `useMultiNetworkPolicy` field:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  useMultiNetworkPolicy: false
```

### Feature Gate Controlled Features

The following features are controlled via OpenShift Feature Gates and cannot be disabled through the Network operator API:

- **Admin Network Policy**: Controlled by `AdminNetworkPolicy` feature gate
- **Network Segmentation (UDN)**: Controlled by `NetworkSegmentation` feature gate

To disable these features, you must configure the cluster's FeatureGate resource. Note that using `CustomNoUpgrade` feature set prevents cluster upgrades.

## Example: Disabling All Optional Features

To disable all optional OVN-Kubernetes features:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  disableMultiNetwork: true
  useMultiNetworkPolicy: false
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      disableEgressFeatures: true
      disableMulticast: true
      disableMultiExternalGateway: true
```

## Use Cases

### DPU Host Deployments

In DPU (Data Processing Unit) host deployments, certain features are not supported on the host side as the networking is offloaded to the DPU. Administrators should disable incompatible features:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      disableEgressFeatures: true
      disableMulticast: true
      disableMultiExternalGateway: true
```

### Minimal Network Configuration

For environments requiring a minimal network stack with reduced resource usage:

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  disableMultiNetwork: true
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      disableEgressFeatures: true
      disableMulticast: true
```

