---
title: "Upgrading from Fission v0.1 to v0.2.x"
date: 2017-09-08T16:26:29-07:00
draft: false
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

1. Get the v0.2 CLI
1. Get your Fission state from your old install
1. Install v0.2.1
1. Restore your Fission state
1. Destroy your old install

### Get the new CLI

```
curl -Lo fission https://...
```

### Get Fission state from v0.1 install

```
fission upgrade dump
```

This will create a JSON file with all your fission state in the current directory.

### Install the new version

Follow the [install guide](../install), but you will need to ensure your two installs don't
conflict. To do that, use separate namespaces and ensure nodeports don't conflict.  Install with a
command similar to this:

```
helm install fission-all --namespace fission2 --set controllerPort=31303,routerPort=31304,natsStreamingPort=31305,functionNamespace=fission2-function
```

### Restore your Fission state into Fission v0.2.1

```
fission upgrade restore
```

This uses the file created in the first step.

### Verify

How exactly you do this is up to you! But, verify that your new install is working.

### Switch over

If you had exposed fission's router to the outside world, switch over to using the new install's router.
   
### Destroy your old install

Once you're no longer using the old install, you can destroy it with:

```
kubectl delete namespace fission fission-function
```

