---
title: "Pre-requisites"
date: 2017-09-07T20:10:05-07:00
draft: false
weight: 22
---

### Cluster preliminaries

If you don't have a Kubernetes cluster, [here's a quick guide to set
one up](../kubernetessetup).

Let's ensure you have the Kubernetes CLI and Helm installed and
ready. If you already have helm, [skip ahead to the fission install](#install-fission).

#### Kubernetes CLI

Ensure you have the Kubernetes CLI.

You can get the Kubernetes CLI for OSX like this:
```
$ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/darwin/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin
```

Or, for Linux:
```
$ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin
```

Ensure you have access to a cluster; use kubectl to check your
Kubernetes version:

```
$ kubectl version
```

We need at least Kubernetes 1.6 (older versions may work, but we don't
test them).

#### Helm

Helm is an installer for Kubernetes.  If you already use helm, [skip to
the next section](#install-fission).

First, you'll need the helm CLI:

On __OS X__:
```
$ curl -LO https://storage.googleapis.com/kubernetes-helm/helm-v2.7.0-darwin-amd64.tar.gz

$ tar xzf helm-v2.7.0-darwin-amd64.tar.gz

$ mv darwin-amd64/helm /usr/local/bin
```

On __Linux__:
```
$ curl -LO https://storage.googleapis.com/kubernetes-helm/helm-v2.7.0-linux-amd64.tar.gz

$ tar xzf helm-v2.7.0-linux-amd64.tar.gz

$ mv linux-amd64/helm /usr/local/bin
```

Next, install the Helm server on your Kubernetes cluster:

```
$ helm init
```
