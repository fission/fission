---
title: "Controlling Function Execution"
draft: false
weight: 43
---

### Autoscaling

Let's create a function to demonstrate the autoscaling behaviour in Fission. We create a simple function which outputs "Hello World" in using NodeJS. We have kept the CPU request and limit purposefully low to simulate the load and also kept the target CPU percent to 50%. 

```
$ fission fn create --name hello --env node --code hello.js --mincpu 10 --maxcpu 40 --minmemory 64 --maxmemory 128 --minscale 1 --maxscale 6 --executortype newdeploy --targetcpu 50
function 'hello' created
```

Now let's use [hey](https://github.com/rakyll/hey) to generate the load with 250 concurrent and a total of 10000 requests:

```
$ hey -c 250 -n 10000 http://$FISSION_ROUTER/hello
Summary:
  Total:	67.3535 secs
  Slowest:	4.6192 secs
  Fastest:	0.0177 secs
  Average:	1.6464 secs
  Requests/sec:	148.4704
  Total data:	160000 bytes
  Size/request:	16 bytes

Response time histogram:
  0.018 [1]	|
  0.478 [486]	|∎∎∎∎∎∎∎
  0.938 [971]	|∎∎∎∎∎∎∎∎∎∎∎∎∎∎
  1.398 [2686]	|∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎
  1.858 [2326]	|∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎
  2.318 [1641]	|∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎
  2.779 [1157]	|∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎∎
  3.239 [574]	|∎∎∎∎∎∎∎∎∎
  3.699 [120]	|∎∎
  4.159 [0]	|
  4.619 [38]	|∎

Latency distribution:
  10% in 0.7037 secs
  25% in 1.1979 secs
  50% in 1.5038 secs
  75% in 2.1959 secs
  90% in 2.6670 secs
  95% in 2.8855 secs
  99% in 3.4102 secs

Details (average, fastest, slowest):
  DNS+dialup:	 0.0058 secs, 0.0000 secs, 1.0853 secs
  DNS-lookup:	 0.0000 secs, 0.0000 secs, 0.0000 secs
  req write:	 0.0000 secs, 0.0000 secs, 0.0026 secs
  resp wait:	 1.6405 secs, 0.0176 secs, 3.6144 secs
  resp read:	 0.0001 secs, 0.0000 secs, 0.0056 secs

Status code distribution:
  [200]	10000 responses

```
While the load is being generated, we will watch the HorizontalPodAutoscaler and how it scales over period of time. As you can notice, the number of pods is scaled from 1 to 3 after the load rises from 8 - 103%. After the load generator stops, it takes a few iterations to scale down from 3 to 1 pod. 

```
$ k -n fission-function get hpa -w
NAME             REFERENCE                   TARGETS    MINPODS   MAXPODS   REPLICAS   AGE
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         1          3m
hello-qoxmothj   Deployment/hello-qoxmothj   8% / 50%   1         6         1         3m
hello-qoxmothj   Deployment/hello-qoxmothj   103% / 50%   1         6         1         4m
hello-qoxmothj   Deployment/hello-qoxmothj   103% / 50%   1         6         3         5m
hello-qoxmothj   Deployment/hello-qoxmothj   25% / 50%   1         6         3         5m
hello-qoxmothj   Deployment/hello-qoxmothj   25% / 50%   1         6         3         6m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         6m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         7m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         7m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         8m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         8m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         9m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         9m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         10m
hello-qoxmothj   Deployment/hello-qoxmothj   5% / 50%   1         6         3         10m
hello-qoxmothj   Deployment/hello-qoxmothj   7% / 50%   1         6         1         11m
hello-qoxmothj   Deployment/hello-qoxmothj   6% / 50%   1         6         1         11m
hello-qoxmothj   Deployment/hello-qoxmothj   6% / 50%   1         6         1         12m
hello-qoxmothj   Deployment/hello-qoxmothj   6% / 50%   1         6         1         12m
```