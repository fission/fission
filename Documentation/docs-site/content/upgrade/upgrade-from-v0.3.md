---
title: "Upgrading from v0.3 to v0.4.x"
date: 2017-11-04T03:38:29+08:00
draft: false
weight: 32
---

## Introduction

Kubernetes ThirdPartyResources ("TPR") are replaced by
CustomResourceDefinitions ("CRD"). TPRs have been deprecated and are
removed in Kubernetes 1.8.

Since Fission stores state in TPRs, we need to migrate this state from
TPRs to CRDs while upgrading.

Follow the instructions below if you're upgrading a Fission 0.2.1 or
0.3.0 cluster to 0.4. If you're using a pre-0.2 Fission cluster, use
the [upgrade guide from 0.1 to 0.2]() and then upgrade to 0.4.0.

## How to Upgrade

1. Get the 0.4.0 CLI
2. Get the Fission state from v0.3 install
3. Upgrade to Fission 0.4.0
4. Upgrade Kubernetes cluster version to 1.7.x or higher
5. Remove all TPR definition (for Kubernetes 1.7.x)
6. Restore Fission state into CRDs

### Get the new CLI

#### OS X

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.4.0/fission-cli-osx && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Linux

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.4.0/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Windows

For Windows, you can use the linux binary on WSL. Or you can download
this windows executable: [fission.exe](https://github.com/fission/fission/releases/download/0.4.0/fission-cli-windows.exe)

### Get Fission state from v0.3 install

```
fission --server <your v0.3 server> tpr2crd dump --file state.json
```

You can skip the --server argument if you have the environment
variable `$FISSION_URL` set to point at a v0.3 Fission server.

This will create a JSON file with all your fission state in the
current directory.

### Upgrade to Fission 0.4.0

Upgrade fission with a command similar to this:

```
helm upgrade fission-all --namespace fission
```

### Upgrade Kubernetes cluster version

Since CustomResource is only supported on Kubernetes v1.7+ and higher, please make sure 
that you upgrade to the right version that supports CustomResource.

### Remove all TPR definition (for Kubernetes 1.7.x)

** NOTICE **: This step will remove TPR definition from your kubernetes cluster. Please make sure that you dump all TPRs at the second step!

Though Kubernetes will migrate TPRs to CRDs automatically when TPR definition is deleted if the same name CRD exists. We still need to make sure that there is no resource gets lost during the migration. Also, since we changed the capitalization of some CRDs to CamelCase (e.g. Httptrigger -> HTTPTrigger), we need to recreate those resources by ourselves.

```
fission tpr2crd delete
```

### Restore your Fission state into Fission 0.4.0

```
fission tpr2crd restore --file state.json
```

This commands needs `$FISSION_URL` set to point to new fission installation.

It uses the file created in the first step.  It doesn't modify state.json.

(Note that you can run this restore on any cluster; it doesn't have the be the same kubernetes
cluster as your old install.)

### Verify

Let's check the migration result, first run following command to check CRD established state.

```
kubectl get crd -o 'custom-columns=NAME:{.metadata.name},ESTABLISHED:{.status.conditions[?(@.type=="Established")].status}'
```

The output should be like this

```
NAME                                 ESTABLISHED
environments.fission.io              True
functions.fission.io                 True
httptriggers.fission.io              True
kuberneteswatchtriggers.fission.io   True
messagequeuetriggers.fission.io      True
packages.fission.io                  True
timetriggers.fission.io              True
```

And check that CRD resources you expect are there.

```
COMMAND:
   fission [resource] list

RESOURCES:
    environments
    functions
    httptriggers
    kuberneteswatchtriggers
    messagequeuetriggers
    packages
    timetriggers
```