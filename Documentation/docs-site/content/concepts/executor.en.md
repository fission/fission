---
title: "Controlling Function Execution"
draft: false
weight: 33
---
# Executors 

When you create a function, you can specify an executor for a function. An executor controls how function pods are created and what capabilities are available for that executor type.

## Pool-based executor

A pool based executor (Refered to as poolmgr) creates a pool of generic environment pods as soon as you create an environment. The pool size of initial "warm" containers can be configured based on user needs. These warm containers contain a small dynamic loader for loading the function. Resource requirements are specified at environment level and are inherited by specialized function pods.

Once you create a function and invoke it, one of pods from the pool is taken out and "specialized" and used for execution. This pod is used for subseqnent requests for that function. If there are no more requests for a certain idle duration, then this pod is cleaned up. If a new requests come after the earlier specialized pod was cleaned up, then a new pod is specialised from the pool and used for execution.

Poolmgr executortype is great for functions where lower latency is a requirement. Poolmgr executortype has certain limitations: for example, you can not autoscale them based on demand.


## New-deployment executor

New-Deployment executor (Newdeploy) creates a Kubernetes Deployment along with a Service and HorizontalPodAutoscaler for function execution. This enables autoscaling of function pods and load balancing the requests between pods. In future additional capabilities will be added for newdeploy executortype such as support for volume etc.  In the new-deploy executor, resource requirements can be specified at the function level. These requirements override those specified in the environment.

Newdeploy executortype can be used for requests with no particular low-latency requirements, such as those invoked asynchronously, minscale can be set to zero. In this case the Kubernetes deployment and other objects will be created on first invocation of the function. Subsequent requests can be served by the same deployment. If there are no requests for certain duration then the idle objects are cleaned up. This mechanism ensures resource consumption only on demand and is a good fit for asynchronous requests.

For requests where latency requirements are stringent, a minscale  greater than zero can be set. This essentially keeps a minscale number of pods ready when you create a function. When the function is invoked, there is no delay since the pod is already created. Also minscale ensures that the pods are not cleaned up even if the function is idle. This is great for functions where lower latency is more important than saving resource consumption when functions are idle.

### The latency vs. idle-cost tradeoff

The executors allow you as a user to decide between latency and a small idle cost tradeoff. Depending on the need you can choose one of the combinations which is optimal for your use case. In future, a more intelligent dispatch mechanism will enable more complex combinations of executors.

| Executor Type | Min Scale| Latency | Idle cost |
|:---------|:---------:|:---------:|:---------|
|Newdeploy|0|High|Very low - pods get cleaned up after idlle time|
|Newdeploy|>0|Low|Medium, Min Scale number of pods are always up|
|Poolmgr|0|Low|Low, pool of pods are always up|

### Autoscaling

The new deployment based executor provides autoscaling for functions based on CPU usage. In future custom metrics will be also supported for scaling the functions. You can set the intial and maximum CPU for a function and target CPU at which autoscaling will be trigerred. Autoscaling is useful for workloads where you expect intermittant spikes in workloads. It also enables optimal usage of resources to execute functions, by using a baseline capacity with minimum scale and ability to burst up to maximum scale based on spikes in demand.