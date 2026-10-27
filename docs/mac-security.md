# MAC security for user-defined networks

When the `MACSecurity` feature gate is enabled, CNO exposes `macSecurity.mode`
in UserDefinedNetwork (UDN) Layer2 and ClusterUserDefinedNetwork (CUDN)
Layer2/Localnet schemas. The gate is enabled in `TechPreviewNoUpgrade` and
`DevPreviewNoUpgrade`, and disabled in `Default`.

- `mode` must be `Enabled` or `Disabled` when `macSecurity` is specified.
- Layer2 supports the field only for secondary networks.
- Disabling MAC spoof protection requires `ipam.mode: Disabled`.
- Omitting the field retains OVN's default MAC spoof protection; the schema
  does not supply a default.
- Existing network immutability rules apply to this field.

CNO installs these CRDs from its own templates. Updating the OVN operand image
alone does not update the schemas. CNO reads the cluster's feature-gate status
and passes `OVN_MAC_SECURITY_ENABLE_API` to the renderer. This template key
controls field exposure and the associated CEL rules; it is not a pod
environment variable and does not gate direct NetworkAttachmentDefinition
configuration.

## Upgrade considerations

The release payload must register `MACSecurity`, and the OVN operand must
implement it. Default clusters retain their existing schemas. Tech Preview
feature sets do not support upgrades or switching back to Default. Rolling
back to a schema without `macSecurity` is not a supported way to restore MAC
protection: the field would no longer be exposed and its value may be pruned
on subsequent writes. Use a separate Default cluster for disabled-gate testing.
