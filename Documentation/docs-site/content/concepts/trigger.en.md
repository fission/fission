---
title: "Trigger"
draft: false
weight: 34
---

Triggers are events that can invoke a [function](../function). Fission has three kinds of triggers that can be used to invoke functions.

## Http Trigger

HTTP triggers enable calling functions with HTTP requests. Supported methods are GET, POST, PUT, DELETE, HEAD and by default GET is used. URL pattern follow the gorilla/mux supported patterns.

## Time Trigger

If you want a function to be called at a periodic frequency then the time triggers are perfect for the use case. Time triggers follow cron like specifications and are invoked based on the cron schedule.

Time trigger based invocations are great for running scheduled jobs, periodic cleanup jobs, periodic polling based invocations etc. 

## MQ Trigger

Message queue based trigger enables ability to listen on a topic and invoke a function for each message. You can optinally send a response to another topic. By default it is assumed that the messages in queue are in application/json format but you can specify otherwise while creating the trigger. Currently `nats-streaming` and `azure-storage-queue` are supported message queues supported.

MQ triggers are great for integrating various systems in a decoupled and asynchronous manner.