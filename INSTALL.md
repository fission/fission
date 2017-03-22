  * [Running Fission on your Cluster](#running-fission-on-your-cluster)
     * [Setup Kubernetes](#setup-kubernetes)
        * [Mac](#install-and-start-kubernetes-on-osx)
        * [Linux](#or-install-and-start-kubernetes-on-linux)
     * [Verify access to the cluster](#verify-access-to-the-cluster)
     * [Get and Run Fission: Minikube or Local cluster](#get-and-run-fission-minikube-or-local-cluster)
     * [Get and Run Fission: GKE or other Cloud](#get-and-run-fission-gke-or-other-cloud)
     * [Install the client CLI](#install-the-client-cli)
     * [Run an example](#run-an-example)
     * [Enable Persistent Function Logs (Optional)](#enable-persistent-function-logs)

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


### Enable Persistent Function Logs (Optional)

Fission uses InfluxDB to store logs and fluentd to forward them from
function pods into InfluxDB.  To setup both InfluxDB and fluentd:

Edit `fission-logger.yaml` to add a username and password for the
Influxdb deployment.  Then:

```
  $ kubectl create -f fission-logger.yaml
```

On the client side,

If you're using minikube or a local cluster:

```
$ export FISSION_LOGDB=http://$(minikube ip):31315
```

If you're using GKE or other cloud:

```
$ export FISSION_LOGDB=http://$(kubectl --namespace fission get svc influxdb -o=jsonpath='{..ip}'):8086
```

That's it for setup.  You can now use this to view function logs:

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
