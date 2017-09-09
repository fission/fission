---
title: "Fission Installation Guide"
date: 2017-09-07T20:10:05-07:00
draft: false
---

Welcome! This guide will get you up and running with Fission on a
Kubernetes cluster.
           
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

#### Set up helm

Helm is an installer for Kubernetes.  If you already use helm, [skip to
the next section](#install-fission).

First, you'll need the helm CLI:

On __OS X__:
```
$ curl -L https://storage.googleapis.com/kubernetes-helm/helm-v2.6.1-darwin-amd64.tar.gz

$ tar xzf helm-v2.6.1-darwin-amd64.tar.gz

$ mv darwin-amd64/helm /usr/local/bin
```

On __Linux__:
```
$ curl -L https://storage.googleapis.com/kubernetes-helm/helm-v2.6.1-linux-amd64.tar.gz

$ tar xzf helm-v2.6.1-linux-amd64.tar.gz

$ mv linux-amd64/helm /usr/local/bin
```

Next, install the Helm server on your Kubernetes cluster:

```
$ kubectl -n kube-system create sa tiller

$ kubectl create clusterrolebinding tiller --clusterrole cluster-admin --serviceaccount=kube-system:tiller

$ helm init --service-account tiller
```

(The first two commands are there to make sure that helm is allowed to
install stuff on Kubernetes, in the common case that your cluster has
role-based access control.)


### Install Fission

#### Minikube

```
$ helm repo add fission-charts https://fission-charts.github.io

$ helm install --namespace fission fission-all --set serviceType=NodePort
```

#### Cloud hosted clusters (GKE, AWS, Azure etc.)

```
$ helm repo add fission-charts https://fission-charts.github.io

$ helm install --namespace fission fission-all
```


#### Minimal version

The fission-all helm chart installs a full set of services including
the NATS message queue, influxDB for logs, etc. If you want a more
minimal setup, you can install the fission-core chart instead.

### Install the Fission CLI

#### Mac OS

Get the CLI binary for Mac:

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/nightly20170705/fission-cli-osx && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Linux

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/nightly20170705/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Windows

For Windows, you can use the linux binary on WSL. Or you can download
this windows executable: [fission.exe](https://github.com/fission/fission/releases/download/nightly20170705/fission-cli-windows.exe)

### Set environment vars

Set the FISSION_URL and FISSION_ROUTER environment variables.
FISSION_URL is used by the fission CLI to find the server.
(FISSION_ROUTER is only needed for the examples below to work.)

#### Minikube

If you're using minikube, use these commands:

```
  $ export FISSION_URL=http://$(minikube ip):31313
  $ export FISSION_ROUTER=$(minikube ip):31314
```
#### Cloud setups

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.  Wait for services to
get IP addresses (check this with ```kubectl --namespace fission get
svc```).  Then:

```
  $ export FISSION_URL=http://$(kubectl --namespace fission get svc controller -o=jsonpath='{..ip}')
  $ export FISSION_ROUTER=$(kubectl --namespace fission get svc router -o=jsonpath='{..ip}')
```

### Run an example

Finally, you're ready to use Fission!

```
$ fission env create --name nodejs --image fission/node-env:v0.2.1

$ curl -LO https://raw.githubusercontent.com/fission/fission/master/examples/nodejs/hello.js

$ fission function create --name hello --env nodejs --code hello.js

$ fission route create --method GET --url /hello --function hello

$ curl http://$FISSION_ROUTER/hello
Hello, world!
```

### What's next?

If something went wrong, we'd love to help -- please [drop by the
slack channel](http://slack.fission.io) and ask for help.

Check out the
[examples](https://github.com/fission/fission/tree/master/examples)
for some example functions.

