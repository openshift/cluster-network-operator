# MAC security Tech Preview testing

This schema update enables testing
[OVN-Kubernetes #6746](https://github.com/ovn-kubernetes/ovn-kubernetes/pull/6746)
with an OpenShift payload containing the matching OVN implementation.
It uses the `MACSecurity` feature gate introduced by
[openshift/api #3062](https://github.com/openshift/api/pull/3062), enabled in
`TechPreviewNoUpgrade` and `DevPreviewNoUpgrade` and disabled in `Default`.

The UDN Layer2 and CUDN Layer2/Localnet schemas accept `macSecurity.mode` with
values `Enabled` or `Disabled`. Layer2 accepts the field only for secondary
networks. Disabling MAC spoof protection requires `ipam.mode: Disabled`.
The existing network immutability rules apply to this field as well.
There is no schema default; omission retains OVN's default protection.

CNO reads the feature gate and passes `OVN_MAC_SECURITY_ENABLE_API` to its
CRD renderer. This is a template key, not an OVN pod environment variable.
When disabled, CNO omits both the `macSecurity` properties and their CEL
rules. Existing network immutability rules remain in both configurations.
This gates UDN/CUDN API exposure, not the direct NAD configuration path.

CNO must carry the schema update: changing the Helm CRDs in an OVN image does
not update the schemas installed by CNO. CNO must also vendor the API revision
containing the gate; `/testwith` does not update Go dependencies.

The API dependency uses the merged official revision `f8795cdde518`, on top
of the Kubernetes 1.37 dependency alignment from
[CNO #3183](https://github.com/openshift/cluster-network-operator/pull/3183).

Use the Tech Preview lane on
[OVN #3460](https://github.com/openshift/ovn-kubernetes/pull/3460):

```text
/testwith openshift/ovn-kubernetes/main/e2e-metal-ipi-core-networking-virt-dualstack-serial-ote-tp openshift/cluster-network-operator#3179
```

The virtualization suite groups the enabled pod, API-validation and VM cases.
The localnet pod cases require VLAN 100 transport between nodes.
The selected payload must include the merged API feature-gate registration;
updating CNO's Go dependency alone does not update payload gate metadata.
Before interpreting results, confirm `FeatureGate/cluster` reports
`MACSecurity` enabled for the running release, the installed UDN/CUDN schemas
contain `macSecurity`, and the OVN operand contains the feature implementation.
The OTE entries must carry `[OCPFeatureGate:MACSecurity]` so they are filtered
on clusters with the gate disabled.

Use a separate Default cluster to verify that the field and its validation
rules are absent and the gated OTE cases are excluded. Do not switch a Tech
Preview cluster back to Default for this comparison.
