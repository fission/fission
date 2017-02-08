# Async Reliable Requests in Fission

Fission needs support for a generic way to issue async reliable
requests.

There are a few components to this:

(a) A message queue.  Based on a good enough feature set and ease of
deployment/operations, we're going with the NATS-Streaming server.

(b) _Subscriptions_: A fission object that represents a subscription
to a NATS subject.

(c) Fission NATS Subscriber: A service that uses the NATS-Streaming
client and invokes fission functions based on the subscriptions.


## NATS Message Queue deployment and operations

 * nats-streaming-server will be deployed on K8s with persistent
   volumes
 * for now we'll just have a yaml file
 * in the future it can be a separate chart that our chart depends on


## Subscription Management

A NATS Subscription is a mapping of a NATS subject to a fission function.

type NATSSubscription struct {
     fission.Metadata
     Subject string
     Function fission.Metadata
}

A Subscriptions CRUD API will be added to the controller, client and
CLI. So for example, user can do

```
fission nats subscribe --subject xxx --function yyy
fission nats unsubscribe --subject xxx --function yyy
```

## Fission NATS Subscriber

A new component will (a) watch the controller for subscriptions and
(b) subscribe accordingly to the NATS streaming server.

The subscription is simple -- a POST request is triggered to a fission
function when a message is received.  The message body is the POST
body.  When the function request completes, the subscriber
acknowledges the message; this lets NATS streaming delete the message
from its buffers; otherwise, it will retain the message and possibly
retry it later.

This means async reliable functions have at-least-once semantics.
They should be implemented idempotently.
