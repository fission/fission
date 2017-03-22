
A high level view of the internals of Fission.

How it works
============

Fission is a FaaS -- users create functions (source level), register
them with fission using a CLI, and associate functions with triggers.

Fission wraps those functions into a service, and runs them on
Kubernetes on demand.

Here's an overview of the services that make up fission.

Components
==========

Language-neutral components:

 * controller
 * poolmgr
 * router
 * kubewatcher

Language-specific components:

 * Environment container


Controller
----------

The controller contains CRUD APIs for functions, http triggers,
environments, Kubernetes event watches.  This is the component that
the client talks to.

This is the only stateful component.  It needs to be configured with a
URL to an etcd cluster and a path to a persistent volume.  The volume
is used to store the functions' source code.  Etcd is used as the DB.

[Work to extend to other storage backends is planned, see issue #83.]

Pool Manager 
------------

Poolmgr manages pools of generic containers and function containers.

It has a simple API; both these endpoints are called by the router.

* GetFunctionService takes function metadata and returns the address
  of a service.
  
* TapService lets poolmgr know a service is being used; if it's not
  called for a few minutes the pod(s) backing the service are killed.

Poolmgr watches the controller API and eagerly creates generic pools
for environments.  It uses Kubernetes deployments to do that.  The
environment container runs in a pod with the 'fetcher' container.
Fetcher is a very simple utility that downloads a URL sent to it and
saves it at a configured location.

GetFunctionService "specializes" a pod.  The implementation chooses a
pod from the pool, relabels it to "orphan" the pod from the
deployment, invokes fetcher to copy the function into the pod, and
hits the the specialize endpoint on the environment container.  This
causes the function to be loaded.  The pod is now specific to that
function.

This function pod is cached; it's cleaned up if it's unused for a few
minutes.

Router
------

The router forwards HTTP requests to function pods.  If there's no
running service for a function, it requests one from poolmgr, while
holding on to the request; when the function's service is ready it
forwards the request.

The router is stateless and can be scaled up if needed, according to
load.

Kubewatcher
-----------

Kubewatcher watches the Kubernetes API and invokes functions
associated with watches, sending the watch event to the function.

The controller keeps track of user's requested watches and associated
functions.  Kubewatcher watches the API based on these requests; when
a watch event occurs, it serializes the object and calls the function
via the router.

While a few simple retries are done, there isn't yet a reliable
message bus between Kubewatcher and the function.  Work for this is
tracked in issue #64.

Environment Container
---------------------

Environment containers run user-defined functions.  Environment
containers are language specific.  They must contain an HTTP server
and a loader for functions.

Poolmgr deploys the environment container into a pod with fetcher
(fetcher is a simple utility that can fetch an HTTP url to a file at a
configured location).  This pod forms a "generic pod", because it can
be loaded with any function.

When poolmgr needs to create a service for a function, it calls
fetcher to fetch the function.  Fetcher downloads the function into a
volume shared between fetcher and this environment container.  Poolmgr
then requests the container to load the function.

Logger
-----------

Logger helps to forward function logs to centralized db service for log
persistence. Currently only influxdb is supported to store logs.
Following is a diagram describe how log service works:

![Logger Diagram](https://cloud.githubusercontent.com/assets/202578/23100399/b0e3ea00-f6ba-11e6-8f2f-6588cfef2e84.png)

1. Pool manager choose a pod from pool to execute user function
2. Pool manager makes a HTTP POST to logger helper, once helper receives 
the request it creates a symlink to container log for fluentd.
3. Fluentd reads log from symlink and pipes to influxdb
4. `fission function logs ...` retrieve event logs from influxdb with 
optional log filter
5. Pool manager removes function pod from pool
6. Pool manager asks logger helper to stop piping logs, logger removes symlink.