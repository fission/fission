Fission: Serverless Functions for Kubernetes
============================================
[![Build Status](https://travis-ci.org/fission/fission.svg?branch=master)](https://travis-ci.org/fission/fission)
[![Go Report Card](https://goreportcard.com/badge/github.com/fission/fission)](https://goreportcard.com/report/github.com/fission/fission)
[![Fission Slack](http://slack.fission.io/badge.svg)](http://slack.fission.io)

[fission.io](http://fission.io)  [@fissionio](http://twitter.com/fissionio)

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.

Fission operates on _just the code_: Docker and Kubernetes are
abstracted away under normal operation, though you can use both to
extend Fission if you want to.

Fission is extensible to any language; the core is written in Go, and
language-specific parts are isolated in something called
_environments_ (more below).  Fission currently supports NodeJS and
Python, with more languages coming soon.

### Performance: 100msec cold start

Fission maintains a pool of "warm" containers that each contain a
small dynamic loader.  When a function is first called,
i.e. "cold-started", a running container is chosen and the function is
loaded.  This pool is what makes Fission fast: cold-start latencies
are typically about 100msec.

### Kubernetes is the right place for Serverless

We're built on Kubernetes because we think any non-trivial app will
use a combination of serverless functions and more conventional
microservices, and Kubernetes is a great framework to bring these
together seamlessly.

Building on Kubernetes also means that anything you do for operations
on your Kubernetes cluster &mdash; such as monitoring or log
aggregation &mdash; also helps with ops on your Fission deployment.


Fission Concepts
----------------

A _function_ is a piece of code that follows the fission function
interface.

An _environment_ contains the language- and runtime-specific parts of
running a function.  Fission comes with NodeJS and Python
environments; you can also extend environments or create entirely new
ones if you want.  (An environment is essentially just a container
with a webserver and dynamic loader.)

A _trigger_ is something that maps an event to a function; Fission
supports HTTP routes as triggers today, with upcoming support for
other types of event triggers, such as timers and Kubernetes events.

Usage
-----

```bash

  # Add the stock NodeJS env to your Fission deployment
  $ fission env create --name nodejs --image fission/node-env

  # A javascript one-liner that prints "hello world"
  $ curl https://raw.githubusercontent.com/fission/fission/master/examples/nodejs/hello.js > hello.js

  # Upload your function code to fission
  $ fission function create --name hello --env nodejs --code hello.js

  # Map GET /hello to your new function
  $ fission route create --method GET --url /hello --function hello

  # Run the function.  This takes about 100msec the first time.
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```

See the [examples](examples) directory for more.

Running Fission on your Cluster
===============================

### Setup Kubernetes

You can install Kubernetes on your laptop with [minikube](https://github.com/kubernetes/minikube):

#### Install and start Kubernetes on OSX:
```bash
  $ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/darwin/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin
  $ curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.16.0/minikube-darwin-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/
  $ minikube start
```

#### Or, install and start Kubernetes on Linux:
```bash
  $ curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin
  $ curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.16.0/minikube-linux-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/
  $ minikube start
```

Or, you can use [Google Container Engine's](https://cloud.google.com/container-engine/) free trial to get a 3 node cluster.

### Verify access to the cluster

```
  $ kubectl version
```

### Get and Run Fission: Minikube or Local cluster

If you're using minikube or no cloud provider, use these commands to
set up services with NodePort.  This exposes fission on ports 31313
and 31314.

```
  $ kubectl create -f http://fission.io/fission.yaml
  $ kubectl create -f http://fission.io/fission-nodeport.yaml
```

Set the FISSION_URL and FISSION_ROUTER environment variables.
FISSION_URL is used by the fission CLI to find the server.
FISSION_URL should be prefixed with a `http://`.  (FISSION_ROUTER is
only needed for the examples below to work.)

If you're using minikube, use these commands:

```
  $ export FISSION_URL=http://$(minikube ip):31313
  $ export FISSION_ROUTER=$(minikube ip):31314
```


### Get and Run Fission: GKE or other Cloud

If you're using GKE or any other cloud provider that supports the
LoadBalancer service type, use these commands:

```
  $ kubectl create -f http://fission.io/fission.yaml
  $ kubectl create -f http://fission.io/fission-cloud.yaml
```

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.  Wait for services to
get IP addresses (check this with ```kubectl --namespace fission get
svc```).  Then:

```
  $ export FISSION_URL=http://$(kubectl --namespace fission get svc controller -o=jsonpath='{..ip}')
  $ export FISSION_ROUTER=$(kubectl --namespace fission get svc router -o=jsonpath='{..ip}')
```
### Get and run fission: OpenShift

If you're using OpenShift, it's possible to run Fission on it! The deployment
template needs to be deployed as a user with cluster-admin permissions (like `system:admin`), as it needs to create a `ClusterRole` for deploying function containers from the `fission` namespace/project.

Identically as with Kubernetes, you need to set the FISSION_URL and FISSION_ROUTER environment variables. If you're using minishift, use these commands:

```
  $ export FISSION_URL=http://$(minishift ip):31313¬
  $ export FISSION_ROUTER=$(minishift ip):31314¬
```

#### Using Minishift or Local Cluster

If you're using minishift or no cloud provider, use these commands to set up services with NodePort. This exposes fission on ports 31313 and 31314.

```
  $ oc login -u system:admin
  $ oc create -f http://fission.io/fission-openshift.yaml
  $ oc create -f http://fission.io/fission-nodeport.yaml
```

#### Using other clouds
If you're using any cloud provider that supports the LoadBalancer service type, use these commands:

```
$ oc login -u system:admin
$ oc create -f http://fission.io/fission-openshift.yaml
$ oc create -f http://fission.io/fission-cloud.yaml
```
After these steps, you should be able to run fission client as with kubernetes.

### Install the client CLI

Get the CLI binary for Mac:

```
  $ curl http://fission.io/mac/fission > fission && chmod +x fission && sudo mv fission /usr/local/bin/
```

Or Linux:

```
  $ curl http://fission.io/linux/fission > fission && chmod +x fission && sudo mv fission /usr/local/bin/
```

### Run an example

Finally, you're ready to use Fission!

```
  $ fission env create --name nodejs --image fission/node-env

  $ curl https://raw.githubusercontent.com/fission/fission/master/examples/nodejs/hello.js > hello.js

  $ fission function create --name hello --env nodejs --code hello.js
  
  $ fission route create --method GET --url /hello --function hello
  
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```

You can also set up persistence for logs: [instructions here](INSTALL.md).


Compiling Fission
=================

[You only need to do this if you're making Fission changes; if you're
just deploying Fission, use fission.yaml which points to prebuilt
images.]

You'll need go installed, along with the [glide dependency management
tool](https://github.com/Masterminds/glide#install).
You'll also need docker for building images.

The server side is compiled as one binary ("fission-bundle") which
contains controller, poolmgr and router; it invokes the right one
based on command-line arguments.

To build fission-bundle: clone this repo to
`$GOPATH/src/github.com/fission/fission`, then from the top level
directory (if you want to build the image with the docker inside
minikube, you'll need to set the proper environment variables with
`eval $(minikube docker-env)`):

```
  # Get dependencies
  $ glide install

  # Build fission server and an image
  $ pushd fission-bundle
  $ ./build.sh

  # Edit push.sh to point to your registry, or comment out the `docker push`
  # line if building into your local minikube for dev purposes
  $ $EDITOR push.sh
  $ ./push.sh
  $ popd

  # To install, update fission.yaml to point to your compiled image
  $ $EDITOR fission.yaml
  $ kubectl create -f fission.yaml
```

If you're changing the CLI:

```
  # Build Fission CLI
  $ cd fission && go install
```

Status
======

Fission is in early alpha.  It's not suitable for production use just
yet.  

We're looking for early developer feedback -- if you do use Fission,
we'd love to hear how it's working for you, what parts in particular
you'd like to see improved, and so on.  Talk to us on
[slack](http://slack.fission.io) or
[twitter](https://twitter.com/fissionio).

