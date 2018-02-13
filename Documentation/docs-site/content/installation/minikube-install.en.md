---
title: "Minikube Installation"
date: 2017-09-07T20:10:05-07:00
draft: false
weight: 23
---

### Install Fission

```
$ helm install --namespace fission --set serviceType=NodePort https://github.com/fission/fission/releases/download/0.5.0/fission-all-0.5.0.tgz
```

The serviceType variable allows configuring the type of Kubernetes
service outside the cluster.  You can use `ClusterIP` if you don't
want to expose anything outside the cluster.

### Install the Fission CLI

#### OS X

Get the CLI binary for Mac:

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.5.0/fission-cli-osx && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Linux

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.5.0/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Windows

For Windows, you can use the linux binary on WSL. Or you can download
this windows executable: [fission.exe](https://github.com/fission/fission/releases/download/0.5.0/fission-cli-windows.exe)

### Set environment vars

Set the FISSION_URL and FISSION_ROUTER environment variables.
FISSION_URL is used by the fission CLI to find the server.
(FISSION_ROUTER is only needed for the examples below to work.)

For minikube, use following commands:

```
  $ export FISSION_URL=http://$(minikube ip):31313
  $ export FISSION_ROUTER=$(minikube ip):31314
```