Fission: Serverless Functions for Kubernetes
============================================
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
  $ echo 'module.exports = function(context, callback) { callback(200, "Hello, world!\n"); }' > hello.js  

  # Upload your function code to fission
  $ fission function create --name hello --env nodejs --code hello.js

  # Map GET /hello to your new function
  $ fission route create --method GET --url /hello --function hello

  # Run the function.  This takes about 100msec the first time.
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```


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

Deploy the fission services and deployments.

```
  $ kubectl create -f http://fission.io/fission.yaml
```

#### local and no cloud provider

If you're using minikube or no cloud provider use the services with NodePort.

```
  $ kubectl create -f http://fission.io/fission-nodeport.yaml
```

#### Cloud

If you're using GKE or any other cloud provider, use the services with a LoadBalancer.

```
  $ kubectl create -f http://fission.io/fission-cloud.yaml
```

### Set fission URLs

```
  $ kubectl --namespace fission get services
```

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively. FISSION_URL should be prefixed with a `http://`.  FISSION_URL is used by
the fission CLI to find the server.  (FISSION_ROUTER is only needed
for the examples below to work.) Below is an example
for Minikube with NodePort.

```
  $ export FISSION_ROUTER=$(minikube ip):31314

  $ export FISSION_URL=http://$(minikube ip):31313
```

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

Fission is in early alpha.  Don't use it in production just yet!

Right now, we're looking for developer feedback -- tell us which
languages you care about, what use cases you might use it for, and so
on.

