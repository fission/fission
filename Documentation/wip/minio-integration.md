# Minio Integration

We want to have changes to a Minio bucket trigger fission functions.
In particular if an object is added to a bucket we want to be able to
trigger a fission function.

That function should be run in an environment that has a minio client
library, so that it can turn around and talk to the Minio API.  The
request context should contain a Minio client handle, or credentials
that would allow the function to connect to Minio.

The Minio-Fission trigger should be async and reliable; we can use the
NATS-based async reliable request infrastructure for this.

## Config Setup

Minio needs to be configured with a NATS endpoint.  So does Fission.
And of course someone needs to deploy the NATS message queue itself.

We can leave all this to the user in a first cut, writing a blog
post/recipe on how to set it up.  In the future when Minio and Fission
both have Helm charts, and NATS does too, we could setup dependencies
and service discovery to have these services discover each other
without explicit configuration.

## Subscription setup

Minio already has NATS integration (though some changes will be needed
for NATS streaming vs. plain NATS).  It also has an API to setup
buckets to send messages into a NATS subject.

There are two possible user stories: the user can setup the
bucket->function subscription from either minio client (mc) or fission
CLI (fission).

With fission, this is how it would look:

```
fission minio subscribe --bucket BUCKET --function FUNCTION
fission minio unsubscribe --bucket BUCKET --function FUNCTION
fission minio list
```

This would result in:

1. A call to Minio to setup notification from the specified bucket to
   a new NATS subject

2. A call to Fission to create a subscription from this bucket into
   the specified function

We can make both the above API calls from the client, OR we could
create some new service on the fission side that does this.  It seems
simpler to just do it on the client side.

### Context Setup

Functions for Minio triggers can be run in regular unmodified fission
environments. However, by changing the environment, we can add to the
context and make it convenient for the programmer to call back into
the Minio API.

Credentials for Minio can be set up as Kubernetes Secrets.  We can the
retrieve those secrets in the function for connecting to the Minio
service.

Each environment has a point where it calls the user function.  We can
add a hook here to connect to Minio and add this connection to the
context object.  User code would then access minio client with
`context.minio`.

### Accessing the object data

Functions can access object data by calling getObject on the API
handle provided by the context.  [Do we want to provide something more
than this?]

