---
title: "Function"
draft: true
weight: 31
---
A function is a piece of code that will be invoked based on a [trigger](../trigger). The code follows the Fission interface. In practice, the function is only an entry point for execution and it can be backed by a larger program or module.

A function is registered with Fission through a CLI and associated with a trigger. A function can be created based on a single source file or a source archive or a deployment archive.

It is possible to associate and use Kubernetes secrets and configmaps with a function.

Functions also accept minimum and maximum CPU and memory to be assigned, the behaviour of which varies based on executor types which are discussed in greater detail [here](../executor)