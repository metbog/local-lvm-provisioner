# Local LVM Provisioner

## Overview

The Local LVM Provisioner has been forked from [Ranger Labs' Local Path Provisioner](https://github.com/rancher/local-path-provisioner) project and aims to provide a simple way to expose local disk storage as LVM volumes on each node. The main motivations for exposing local storage as logical volumes instead of simple `hostPath` mounts was to enforce size limits
and to gain the ability to do atomic snapshots of the data for backup purposes.

Depending on the user provided configuration, the Local LVM Provisioner will create a Logical Volume in one of the supplied Volume Groups on demand, format it using the ext4 filesystem, mount it on the host system and expose the host system mount point to the container using a `hostPath` based persistent volume.

As Rangers's Local Path Provisioner, the LVM provisioning relies on the features introduced by Kubernetes [Local Persistent Volume feature](https://kubernetes.io/blog/2018/04/13/local-persistent-volumes-beta/), but aims to be a simpler solution than the built-in `local` volume feature in Kubernetes.

## Compare to built-in Local Persistent Volume feature in Kubernetes

### Pros

1. Dynamic creation and provisioning of LVM volumes from free host volume group space
    1. Currently the Kubernetes [Local Volume provisioner](https://github.com/kubernetes-incubator/external-storage/tree/master/local-volume) cannot do dynamic provisioning for the host path volumes
2. Ability to perform LVM operations on the provisioned space, e.g. snapshotting to backup data

### Cons

1. Only supports `hostPath` based mounts

## Requirement
Kubernetes v1.12+.

## Deployment

### Installation

In this setup, the directory `/data` will be used across all the nodes as the path for creating the volume mount points and a LVM volume group named `vg1` will be used to allocate logical volumes in. The provisioner will be installed in the `local-lvm-storage` namespace by default.

```
kubectl apply -f https://gist.githubusercontent.com/jow-/34991ba57e8993d6abf89483afc0bb5d/raw/14e92b01431610e7e462dd451ba0d17ec9fbb9b5/local-lvm-storage.yaml
```

After installation, you should see something like the following:
```
$ kubectl -n local-lvm-storage get pod
NAME                                     READY   STATUS    RESTARTS   AGE
local-lvm-provisioner-75dfd97848-db2rq   1/1     Running   0          34m
```

Check and follow the provisioner log using:
```
$ kubectl -n local-lvm-storage logs -f local-lvm-provisioner-75dfd97848-db2rq
```

## Usage

Create a `hostPath` backed Persistent Volume and a pod uses it:

```
kubectl create -f https://raw.githubusercontent.com/jow-/local-lvm-provisioner/master/examples/pvc.yaml
kubectl create -f https://raw.githubusercontent.com/jow-/local-lvm-provisioner/master/examples/pod.yaml
```

You should see the PV has been created:
```
$ kubectl get pv
NAME                                       CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS   CLAIM                    STORAGECLASS   REASON   AGE
pvc-5671a2cd-6b25-11e9-acd6-901b0ec4277a   2Gi        RWO            Delete           Bound    default/local-lvm-pvc    local-lvm               4s
```

The PVC has been bound:
```
$ kubectl get pvc
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
local-lvm-pvc   Bound    pvc-5671a2cd-6b25-11e9-acd6-901b0ec4277a   2Gi        RWO            local-lvm      5s
```

And the Pod started running:
```
$ kubectl get pod
NAME          READY     STATUS    RESTARTS   AGE
volume-test   1/1       Running   0          6s
```

Write something into the pod
```
kubectl exec volume-test -- sh -c "echo local-lvm-test > /data/test"
```

Now delete the pod using
```
kubectl delete -f https://raw.githubusercontent.com/jow-/local-lvm-provisioner/master/examples/pod.yaml
```

After confirm that the pod is gone, recreated the pod using
```
kubectl create -f https://raw.githubusercontent.com/jow-/local-lvm-provisioner/master/examples/pod.yaml
```

Check the volume content:
```
$ kubectl exec volume-test cat /data/test
local-lvm-test
```

Delete the pod and pvc
```
kubectl delete -f https://raw.githubusercontent.com/jow-/local-lvm-provisioner/master/examples/pod.yaml
kubectl delete -f https://raw.githubusercontent.com/jow-/local-lvm-provisioner/master/examples/pvc.yaml
```

The volume content stored on the node will be automatically cleaned up. You can check the log of `local-lvm-provisioner-xxx` for details.

Now you've verified that the provisioner works as expected.

## Configuration

The configuration of the provisioner is a json file `config.json`, stored in the a config map, e.g.:
```
kind: ConfigMap
apiVersion: v1
metadata:
  name: local-lvm-config
  namespace: local-lvm-storage
data:
  config.json: |-
        {
                "nodeVGMap":[
                {
                        "node":"DEFAULT_VGS_FOR_NON_LISTED_NODES",
                        "path":"/data",
                        "vgs":["vg1"]
                },
                {
                        "node":"yasker-lp-dev1",
                        "path":"/volumes",
                        "vgs":["vg0", "vg1"]
                },
                {
                        "node":"yasker-lp-dev3",
                        "path":"/volumes",
                        "vgs":[]
                }
                ]
        }

```

### Definition

The `nodeVGMap` array is the place user can customize where to store the data on each node.

1. If one node is not listed on the `nodeVGMap`, and Kubernetes wants to create volume on it, the paths specified in `DEFAULT_VGS_FOR_NON_LISTED_NODES` will be used for provisioning.
2. If one node is listed on the `nodeVGMap`, the specified volume groups in `vgs` will be used for provisioning and mount point directories named after the namespace and name of the claim will be created below the directory specified by `path`.
    1. If one node is listed but with `vgs` set to `[]`, the provisioner will refuse to provision on this node.
    2. If more than one volume group was specified, the path would be chosen randomly when provisioning.

### Rules

The configuration must obey following rules:

1. The `config.json` key of the config map must valid JSON.
2. A path must start with `/`, a.k.a an absolute path.
2. Root directory(`/`) is prohibited.
3. No duplicate volume groups are allowed for one node.
4. No duplicate nodes are allowed.

### Reloading

The provisioner supports automatic reloading of configuration. Users can change the configuration using `kubectl apply` or `kubectl edit` with config map `local-lvm-config`. It will be a delay between user update the config map and the provisioner pick it up.

When the provisioner detected the configuration changes, it will try to load the new configuration. Users can observe it in the log

```
time="2018-10-03T05:56:13Z" level=debug msg="Applied config: {\"nodeVGMap\":[{\"node\":\"DEFAULT_VGS_FOR_NON_LISTED_NODES\",\"path\":\"/data\",\"vgs\":[\"vg1\"]},...]}"
```

If the reload failed due to some reason, the provisioner will report error in the log, and **continue using the last valid configuration for provisioning in the meantime**.

```
time="2018-10-03T05:19:25Z" level=error msg="failed to load the new config file: fail to load config file /etc/config/config.json: invalid character '#' looking for beginning of object key string"
time="2018-10-03T05:20:10Z" level=error msg="failed to load the new config file: config canonicalization failed: path must start with / for path opt on node yasker-lp-dev1"
time="2018-10-03T05:23:35Z" level=error msg="failed to load the new config file: config canonicalization failed: duplicate path /data1 on node yasker-lp-dev1
time="2018-10-03T06:39:28Z" level=error msg="failed to load the new config file: config canonicalization failed: duplicate node yasker-lp-dev3"
```

## Uninstall

Before uninstallation, make sure that the PVs created by the provisioner have already been deleted. Use `kubectl get pv` and make sure no PV with StorageClass `local-lvm` exists anymore.

To uninstall, execute:

```
kubectl delete -f https://gist.githubusercontent.com/jow-/34991ba57e8993d6abf89483afc0bb5d/raw/14e92b01431610e7e462dd451ba0d17ec9fbb9b5/local-lvm-storage.yaml
```

Note that since v0.10.2, the provisioner will install a systemd unit file and helper script to remount any required LVM volumes on boot.
To remove these, issue the following commands:

```
rm /usr/local/bin/local-lvm-provisioner
systemctl disable local-lvm-provisioner
rm /etc/systemd/system/local-lvm-provisioner.service
systemctl daemon-reload
```

## License

Copyright (c) 2019  [Jo-Philipp Wich](http://mein.io/)<br>
Copyright (c) 2014-2018  [Rancher Labs, Inc.](http://rancher.com/)

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License. You may obtain a copy of the License at

[http://www.apache.org/licenses/LICENSE-2.0](http://www.apache.org/licenses/LICENSE-2.0)

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions and limitations under the License.
