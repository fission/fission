
- [Running Fission on your Cluster](#running-fission-on-your-cluster)
  * [Setup Kubernetes](#setup-kubernetes)
    + [Install and start Kubernetes on OSX:](#install-and-start-kubernetes-on-osx)
    + [Or, install and start Kubernetes on Linux:](#or-install-and-start-kubernetes-on-linux)
  * [Verify access to the cluster](#verify-access-to-the-cluster)
  * [Get and Run Fission: Minikube or Local cluster](#get-and-run-fission-minikube-or-local-cluster)
  * [Get and Run Fission: GKE or other Cloud](#get-and-run-fission-gke-or-other-cloud)
  * [Get and Run Fission: OpenShift](#get-and-run-fission-openshift)
    + [Using Minishift or Local Cluster](#using-minishift-or-local-cluster)
    + [Using other clouds](#using-other-clouds)
  * [Install the client CLI](#install-the-client-cli)
  * [Run an example](#run-an-example)
  * [Enable Persistent Function Logs (Optional)](#enable-persistent-function-logs-optional)
  * [Use the web based Fission-ui (Optional)](#use-the-web-based-fission-ui-optional)

## Running Fission on your Cluster

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
  $ kubectl create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-rbac.yaml
  $ kubectl create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-nodeport.yaml
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
  $ kubectl create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-rbac.yaml
  $ kubectl create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-cloud.yaml
```

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.  Wait for services to
get IP addresses (check this with ```kubectl --namespace fission get
svc```).  Then:

```
  $ export FISSION_URL=http://$(kubectl --namespace fission get svc controller -o=jsonpath='{..ip}')
  $ export FISSION_ROUTER=$(kubectl --namespace fission get svc router -o=jsonpath='{..ip}')
```

### Get and Run Fission: OpenShift

If you're using OpenShift, it's possible to run Fission on it! The
deployment template needs to be deployed as a user with cluster-admin
permissions (like `system:admin`), as it needs to create a
`ClusterRole` for deploying function containers from the `fission`
namespace/project.

#### Using Minishift or Local Cluster

If you're using minishift or no cloud provider, use these commands to set up services with NodePort. This exposes fission on ports 31313 and 31314.

```
  $ oc login -u system:admin
  $ oc adm policy add-cluster-role-to-user cluster-admin developer
  $ oc login -u developer
  $ oc create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-openshift.yaml
  $ oc create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-cloud.yaml
  $ oc project fission
  $ oc expose service router --port 8888
  $ oc expose service controller --port 8888
```

#### Using other clouds

If you're using any cloud provider that supports the LoadBalancer service type, use these commands:

```
$ oc login -u system:admin
$ oc create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-openshift.yaml
$ oc create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-cloud.yaml
```

Identically as with Kubernetes, you need to set the FISSION_URL and FISSION_ROUTER environment variables. If you're using minishift, use these commands:

```
  $ export FISSION_URL=http://$(oc export route/controller -o json | jq -r '.spec.host')¬
  $ export FISSION_ROUTER=$(oc export route/router -o json | jq -r '.spec.host')¬
```
After these steps, you should be able to run fission client as with kubernetes.

### Install the client CLI

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


### Enable Persistent Function Logs (Optional)

Fission uses InfluxDB to store logs and fluentd to forward them from
function pods into InfluxDB.  

```
  $ kubectl create -f fission-logger.yaml
```

That's it for the basic setup.  You can now use following command to view function logs:

```
  $ fission function logs --name hello
```

You can also list the all the pods that have hosted the function
(including ones that aren't alive any more) and view logs for a
particular pod:

```
  $ fission function pods --name hello

  $ fission function logs --name hello --pod <pod name>
```

### Use the web based Fission-ui (Optional)

[Fission-ui](https://github.com/fission/fission-ui) is the ui for fission maintained by the community.
It allows users to observe and manage fission. It also provides a simple online development environment for serverless functions.

To setup Fission-ui with fission in k8s is simple:

```bash
  # Run this after fission is deployed
  $ kubectl create -f https://raw.githubusercontent.com/fission/fission-ui/master/docker/fission-ui.yaml
```

Then open `http://node-ip:31319` to use Fission-ui.

For more infomation, please check out [Fission-ui Readme](https://github.com/fission/fission-ui/blob/master/README.md).

### Install NATS for message-queue based triggers (Optional)

Fission supports message queue triggers that allow you to invoke
functions based on events in a queue.  For now, NATS-Streaming is the
only supported message queue.

You can install NATS Streaming on your Kubernetes cluster with:

```
  $ kubectl create -f https://github.com/fission/fission/releases/download/nightly20170705/fission-nats.yaml
```

You can subscribe to a NATS Streaming queue with a command like this:
(See `fission mqtrigger --help` for details)

```
  $ fission mqtrigger create --name myQueueTrigger --function processEvent --topic "myQueue.request" 
```
