---
title: "Upgrading from v0.1 to v0.2.x"
draft: false
weight: 31
---

## TL;DR

The Fission API has changed significantly in this version.  The new API is incompatible with the
old one.  The CLI is compatible; if you wrote scripts using it, those should still work.

Below we describe a tool for migrating your state from your old install to the new one.

While this upgrade is going to be disruptive, we're going to do our best to make sure future
upgrades aren't as bad.

## Why is this so complicated?

For a couple of reasons, we wanted to switch to using Kubernetes resources (ThirdPartyResources
now, CustomResources in the next release) for storing Fission state: (a) it would allow users to
avoid management of another database and (b) Fission would fit better into the Kubernetes
ecosystem.

Concurrently with this change, we were also trying to make our versioning approach less
opinionated, so it would work with other tools.

Thirdly, we were also enabling build pipelines (v2 Environments).

These changes, especially the difference in versioning approach, made maintaining compatiblity not
worth the effort at this early stage of the project.

All that said, we want you to know that we care a lot about compatiblity, and we'll be more
rigorous about it from the beta release onwards.

## How to Upgrade

1. Get the v0.2.1 CLI
1. Get the Fission state from your old install
1. Install Fission v0.2.1
1. Restore Fission state into your new install
1. Destroy your old install

### Get the new CLI

#### OS X

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/v0.2.1-rc/fission-cli-osx && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Linux

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/v0.2.1-rc/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Windows

For Windows, you can use the linux binary on WSL. Or you can download
this windows executable: [fission.exe](https://github.com/fission/fission/releases/download/v0.2.1-rc/fission-cli-windows.exe)

### Get Fission state from v0.1 install

```
fission --server <your V1 server> upgrade dump --file state.json
```

You can skip the --server argument if you have the environment
variable `$FISSION_URL` set to point at a v0.1 Fission server.

This will create a JSON file with all your fission state in the
current directory.

### Install the new version

Read the [install guide](../install).  You can follow all of it, except that you will need to
ensure your two installs don't conflict.  To do that, use separate namespaces and ensure nodeports
don't conflict.  Install with a command similar to this:

```
helm install fission-all --namespace fission2 --set controllerPort=31303,routerPort=31304,natsStreamingPort=31305,functionNamespace=fission2-function
```

This installs fission in the `fission2` namespace and runs functions
in the `fission2-function` namespace.

### Restore your Fission state into Fission v0.2.1

```
fission upgrade restore --file state.json
```

This commands needs $FISSION_URL set to point to new fission installation.

It uses the file created in the first step.  It doesn't modify state.json.

(Note that you can run this restore on any cluster; it doesn't have the be the same kubernetes
cluster as your old install.)

### Verify

How exactly you do this is up to you! But, at a minimum, run `fission
fn list` to check that all the functions you expect are there.

### Switch over

If you had exposed fission's router to the outside world, switch over to using the new install's router.
   
### Destroy your old install

Once you're no longer using the old install, you can destroy it by
deleting the namespaces that was installed in.

```
kubectl delete namespace fission fission-function
```

