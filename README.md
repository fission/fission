Fission: Serverless Functions for Kubernetes
============================================

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.  See http://fission.io
for more.

Fission operates on just code: you don't deal with Docker or
Kubernetes, they're abstracted away.

We're built on Kubernetes because we think any non-trivial app will
use a combination of serverless functions and more conventional
microservices, and Kubernetes is a great framework to bring these
together seamlessly.

Fission maintains a pool of "warm" containers that each contain a
small dynamic loader.  When a function is first called,
i.e. "cold-started", a running container is chosen and the function is
loaded.  This pool is what makes Fission fast: cold-start latencies
are typically about 100msec.

Fission is extensible to any language.  It currently supports NodeJS
and Python, with more languages coming soon.

Fission Concepts
================

A _function_ is a piece of code with an entry point.

An _environment_ is a container with a webserver and dynamic loader
for functions.  Today, Fission comes with NodeJS and Python
environments and you can also add your own.  So for example if you
want to add some binaries to your Python image and call them from your
code, you can edit the Python environment's Dockerfile, rebuild it,
and add it to Fission.

A _trigger_ is something that maps an event to a function; Fission
supports HTTP triggers today, with upcoming support for other types of
event triggers.


Running Fission on your Cluster
===============================

### Setup Kubernetes

You can install Kubernetes on your laptop with minikube: https://github.com/kubernetes/minikube

Or, you can use Google Container Engine's free trial to get a 3 node cluster.


### Verify access to the cluster

```
  $ kubectl version
```

### Get and Run Fission

If you're using GKE, use the fission.yaml unmodified.  If you're using
minikube, change all instances of LoadBalancer services to NodePort.

```
  $ curl http://fission.io/fission.yaml | kubectl create -f -

  $ kubectl --namespace fission get services
```

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.  FISSION_URL is used by
the fission CLI to find the server.  (FISSION_ROUTER is only needed
for the examples below to work.)

### Install the client CLI

```
  $ curl http://fission.io/fission > fission

  $ chmod +x fission

  $ sudo mv fission /usr/local/bin/
```

### Run an example

```
  $ fission env create --name nodejs --image fission/node-env
  
  $ echo 'module.exports = function(context, callback) { callback(200, "Hello, world!\n"); }' > hello.js  

  $ fission function create --name hello --env nodejs --code hello.js
  
  $ fission route create --method GET --url /hello --function hello
  
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```

Compiling Fission
=================

[You only need to do this if you're making Fission changes; if you're
just deploying Fission, use fission.yaml which points to prebuilt
images.]

You'll need go installed, along with the glide dependecy management
tool.  You'll also need docker for building images.

The server side is compiled as one binary ("fission-bundle") which
contains controller, poolmgr and router; it invokes the right one
based on command-line arguments.

To build fission-bundle: clone this repo, then from the top level
directory:

```
  # Get dependencies
  $ glide install

  # Build fission server and an image
  $ pushd fission-bundle
  $ ./build.sh

  # Edit push.sh to point to your registry
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
  $ cd fission-cli && go install
```

Status
======

Fission is in early alpha.  Don't use it in production just yet.
We're looking for developer feedback -- tell us which languages you
care about, what use cases you might use it for, and so on.

