---
title: "Kubernetes Quick Install"
date: 2017-09-07T20:10:05-07:00
draft: true
---

This is a quick guide to help you get started running Kubernetes on
your laptop (or on the cloud).

(This isn't meant as a production Kuberenetes guide; it's merely
intended to give you something quickly so you can try Fission on it.)

## Minikube

Minikube is the simplest way to run Kubernetes on your laptop.

### Install and start Kubernetes on OSX:

```
$ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/darwin/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin

$ curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.16.0/minikube-darwin-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/

$ minikube start
```

### Or, install and start Kubernetes on Linux:

```
$ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin

$ curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.16.0/minikube-linux-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/

$ minikube start
```

## Google Container Engine

You can use [Google Container Engine's](https://cloud.google.com/container-engine/) free trial to
get a 3-node cluster.  Hop over to [Google Cloud](https://cloud.google.com/container-engine/) to set that up.

