---
title: "Executor Types"
date: 2018-02-01T23:10:05-07:00
draft: false
weight: 41
---
## Executors and ExecutorType

When you create a function, you can specify an executortype for a function. An executortype controls how function pods are created and what capabilities are available for that executor type. 

Poolmgr is the default executortype for functions and has been part of Fission since inception. NewDeploy is a new executortype added in v0.5.0 and is in alpha state. Following sections detail each of executor types and what capabilities are available to the user.

### Resource requirements

From release 0.5.0 you can also specify resource requirements for function pods. The behaviour varies for both executortypes and is explained in respective section.

### Pool Manager (poolmgr)

A pool manager executortype (Refered to as poolmgr) creates a pool of generic environment pods as soon as you create an environment. The pool size of initial "warm" containers can be configured based on user needs. These warm containers contain a small dynamic loader.

Resource requirements are specified at environment level and are inherited by specialized function pods. 

Once you create a function and invoke it, one of pods from the pool is taken out and "specialized" and used for execution. This pod is used for subseqnent requests in a serialized manner. If there are no more requests for a certain idle duration, then this pod is cleaned up. If a new requests come after the earlier specialized pod was cleaned up, then a new pod is specialised from the pool and used for execution.

Poolmgr executortype is great for functions where lower latency is a requirement. Poolmgr executortype has certain limitations for example you can not autoscale them based on demand.


### New Deploy (newdeploy)

NewDeploy executortype creates a Kubernetes Deployment along with a Service and HorizontalPodAutoscaler for function execution. This enables autoscaling of function pods and load balancing the requests between pods. In future additional capabilities can be added for newdeploy executortype such as support for volume etc. 

In newdeploy executortype resources requirements are specified at environment level but can be overridden at function level. 

Newdeploy executortype can be used for async as well as real time requests by tuning the minscale parameter as explained below.

For async requests minscale can be maintained to zero. In this case the Kubernetes deployment and other objects will be created on first invocation of the function. Subsequent requests can be served by the same deployment. If there are no requests for certain duration then the idle objects are cleaned up. This mechanism ensures resource consumption only on demand and is a good fit for asynchronous requests.

For requests where latency requirements are stringent, a minscale  greater than zero can be maintained. This essentially keeps a minscale number of pods of ready when you create a function. There is no "delay" in creation and hence faster response time can be guranteed. Also minscale ensures that the pods are not cleaned up during cleanup operation. This is great for functions where lower latency is important over a relatively additional consumption of resources in form of pre-created pods. 

#### Scaling 

Another thing that newdeploy executortype supports is autoscaling of function pods. The autoscaling is based on the V1 of AutoScaling of Kubernetes hence only supports scaling based on CPU as of today. When creating a function you can specify target CPU percetnage (Default to 80%) at which autoscaling will be triggered. In future as the V2 of AutoScaling stabilizes support can be added for for custom metrics for autoscaling. 
