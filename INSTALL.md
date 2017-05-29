# Installation Guide

The installation of Fission generally consists of three main aspects.
First, the user needs to ensure that there is a Kubernetes cluster available, whether it is locally or remote.
Second, Fission needs to be installed on top of the Kubernetes cluster.
Third, the user needs the Fission CLI in order to be able to interact with the Fission platform. 
This guide is intended to provide a detailed description of this entire installation process.

- [Prerequisites](#prerequisites)
  - [Kubernetes](#kubernetes)
	  - [Minikube](#minikube)
	  - [Hosted Options](#hosted-options)
- [Setup the Fission Platform](#setup-the-fission-platform)
  - [Local Cluster](#local-cluster)
  - [OpenShift](#openshift)
  - [GKE and other Cloud Providers](#gke-and-other-cloud-providers)
- [Install the Fission Client CLI](#install-the-fission-client-cli)
- [Verify the Setup](#verify-the-setup)
- [Optional Components](#optional-components)
  - [Setup Persistent Function Logging](#setup-persistent-function-logging)
  - [Setup the Web-Based Fission UI](#setup-the-web-based-fission-ui)

## Prerequisites

### Kubernetes

Currently, the main prerequisite for Fission is a accessible Kubernetes cluster.
In order to verify that there is access to a cluster, run:

```bash
  $ kubectl version
```

If the result shows that the client and server are working correctly, you can skip the remainder of this section.
Otherwise, follow one of the Kubernetes installation instructions below.
Afterwards run the command above again, to verify that the cluster is accessible.

#### Minikube
You can install Kubernetes on your local machine with [minikube](https://github.com/kubernetes/minikube):

Install and start Kubernetes on **OSX**:

```bash
  $ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/darwin/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin
  $ curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.16.0/minikube-darwin-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/
  $ minikube start
```

Or, install and start Kubernetes on **Linux**:

```bash
  $ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin
  $ curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.16.0/minikube-linux-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/
  $ minikube start
```

#### Hosted Options

In case that a local deployment is undesirable, there are various cloud providers offering (free) hosted Kubernetes clusters.
You can use [Google Container Engine's](https://cloud.google.com/container-engine/) free trial to get a 3 node cluster.
Alternatively, for all other options, check the [Kubernetes Setup Guide](https://kubernetes.io/docs/setup/pick-right-solution/).

## Setup the Fission Platform

Given that all prerequisites have been met, it is time setup the Fission platform.
This consists of deploying all required components on to the Kubernetes cluster, such as the router, API controller and pool manager.

### Local Cluster

If you're using minikube or no cloud provider, use these commands to set up services with NodePort. 
This exposes fission on ports 31313 and 31314.

```bash
  $ kubectl create -f http://fission.io/fission.yaml
  $ kubectl create -f http://fission.io/fission-nodeport.yaml
```

Set the `FISSION_URL` and `FISSION_ROUTER` environment variables. 
`FISSION_URL` is used by the fission CLI to find the server. 
`FISSION_URL` should be prefixed with a `http://`. 
(`FISSION_ROUTER` is only needed for the examples below to work.)

If you're using minikube, use these commands:

```bash
  $ export FISSION_URL=http://$(minikube ip):31313
  $ export FISSION_ROUTER=$(minikube ip):31314
```

### OpenShift

If you're using OpenShift, it's possible to run Fission on it! 
The deployment template needs to be deployed as a user with cluster-admin permissions (like `system:admin`), as it needs to create a `ClusterRole` for deploying function containers from the `fission` namespace/project.

Identically as with Kubernetes, you need to set the `FISSION_URL` and `FISSION_ROUTER` environment variables. 
If you're using minishift, use these commands:

```bash
  $ export FISSION_URL=http://$(minishift ip):31313
  $ export FISSION_ROUTER=$(minishift ip):31314
```

### GKE and other Cloud Providers

If you're using Google Kubernetes Environment (GKE) or any other cloud provider that supports the LoadBalancer service type, use these commands:

```bash
  $ kubectl create -f http://fission.io/fission.yaml
  $ kubectl create -f http://fission.io/fission-cloud.yaml
```

Save the external IP addresses of controller and router services in `FISSION_URL` and `FISSION_ROUTER`, respectively. 
Wait for services to get IP addresses (check this with `kubectl --namespace fission get svc`). 
Then run:

```bash
  $ export FISSION_URL=http://$(kubectl --namespace fission get svc controller -o=jsonpath='{..ip}')
  $ export FISSION_ROUTER=$(kubectl --namespace fission get svc router -o=jsonpath='{..ip}')
```

### Install the Fission Client CLI

Get the CLI binary for Mac:

```bash
  $ curl http://fission.io/mac/fission > fission && chmod +x fission && sudo mv fission /usr/local/bin/
```

Or, for Linux:

```bash
  $ curl http://fission.io/linux/fission > fission && chmod +x fission && sudo mv fission /usr/local/bin/
```

## Verify the Setup

After following the previous steps, you're ready to use Fission!  
In order to verify that Fission has been setup correctly, let's setup and invoke a small example function.

```bash
  # Create an NodeJS environment, to allow for the execution of javascript functions.
  $ fission env create --name nodejs --image fission/node-env
  
  # Fetch the sample function, which just prints 'hello world' on every invocation.
  $ curl https://raw.githubusercontent.com/fission/fission/master/examples/nodejs/hello.js > hello.js
  
  # Using the nodejs environment, deploy hello.js has a Fission function, named 'hello'.
  $ fission function create --name hello --env nodejs --code hello.js
  
  # Setup the 'hello/' route from the API router to the 'hello' function.
  $ fission route create --method GET --url /hello --function hello
```

You should now be able to call the endpoint 'hello/' we set up.

```bash
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```

If the setup of Fission went fine, the invocation will print `Hello, world!`.

## Optional Components

Fission is intended to have a small, essential core, while being highly extensible.
For this reason, there various existing and planned components that can be added to a Fission deployment.
Unless stated otherwise, the remainder of this section assumes that you have a working Fission deployment.t

### Setup Persistent Function Logging

Fission uses InfluxDB to store logs and Fluentd to forward them from function pods into InfluxDB. 

In order to setup both InfluxDB and Fluentd, first edit `fission-logger.yaml` to add a username and password for the InfluxDB deployment.

Then create the the fission-logger deployment: 
```bash
  $ kubectl create -f fission-logger.yaml
```

On the client side,

If you're using minikube or a local cluster:

```bash
$ export FISSION_LOGDB=http://$(minikube ip):31315
```

If you're using GKE or other cloud:

```bash
$ export FISSION_LOGDB=http://$(kubectl --namespace fission get svc influxdb -o=jsonpath='{..ip}'):8086
```

That's it for setup. You can now use this to view function logs:

```bash
  $ fission function logs --name hello
```

You can also list the all the pods that have hosted the function (including ones that are not alive any more) and view logs for a particular pod:

```bash
  $ fission function pods --name hello

  $ fission function logs --name hello --pod <pod name>
```

### Setup the Web-Based Fission UI

[Fission-ui](https://github.com/fission/fission-ui) is the UI for fission maintained by the community. 
It allows users to observe and manage fission. 
Additionally, it also provides a simple online development environment for serverless functions.

To setup Fission-ui with fission in Kubernetes, create the following deployment:

```bash
  $ kubectl create -f https://raw.githubusercontent.com/fission/fission-ui/master/docker/fission-ui.yaml
```

Then open `http://<node-ip>:31319` to use Fission-ui.

For more information, please check out [Fission-ui README](https://github.com/fission/fission-ui/blob/master/README.md).
