North-South IPsec
==================

N-S IPsec allow creating ipsec tunnels in/out of the cluster.

Prerequsits:
-------------
1. Enable ipsec os/extension
```
for role in master worker; do
cat >> "${SHARED_DIR}/manifest_${role}-ipsec-extension.yml" <<-EOF
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: $role
  name: 80-$role-extensions
spec:
  config:
    ignition:
      version: 3.2.0
  extensions:
    - ipsec
EOF
done
```

2. optionally enable ipsec service. this probably needed only on workers, but depends on use case
```
for role in master worker; do
cat >> "${SHARED_DIR}/manifest_${role}-ipsec-extension.yml" <<-EOF
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: $role
  name: 80-$role-ipsec-enable-service
spec:
  config:
    ignition:
      version: 3.2.0
  systemd:
    units:
    - name: ipsec.service
      enabled: true
EOF
done

```

3. import external cert to NSS
use butane to compile MCs out of this:
```
variant: openshift
version: 4.14.0
metadata:
  name: 99-$role-ipsec-import-certs
storage:
  files:
  - path: /etc/pki/certs/cacert.p12
    mode: 0400
    overwrite: true
    contents:
      local: cacert.p12
  - path: /etc/pki/certs/cert.p12
    mode: 0400
    overwrite: true
    contents:
      local: cert.p12
  - path: /usr/local/bin/ipsec-addcert.sh
    mode: 0740
    overwrite: true
    contents:
      inline: |
        #!/bin/bash
        echo "importing cert to NSS"
        certutil -A -a -i /etc/pki/certscacert.p12 -d sql:/var/lib/ipsec/nss -n "CAname" -t 'CT,,'
        certutil -A -a -i /etc/pki/certscert.p12 -d sql:/var/lib/ipsec/nss -n "Certname" -t 'CT,,'
  systemd:
     units:
     - name: ipsec-import-certs
       enabled: true
       contents: |
         [Unit]
         Description=Import external certs into ipsec NSS
         Before=ipsec.service
         
         [Service]
         Type=oneshot
         ExecStart=/usr/local/bin/ipsec-addcert.sh
         RemainAfterExit=false
         StandardOutput=journal

         [Install]
         WantedBy=multi-user.target
```

4. create your ipsec conf
      

