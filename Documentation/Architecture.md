
A high level view of the internals of Fission.

Components
==========

Language-neutral components:

 * Controller
 * Container Pool Manager
 * Container Specializer
 * Router

Language-specific components:

 * Language Build Container
 * Language Run Container


Controller
----------

Function and Trigger CRUD APIs.  APIs to watch for changes are also
included (useful for other components that cache state).

See api/swagger.json for API details.

This is the only stateful component.  It needs to be configured with a
URL to an etcd cluster and a path to a persistent volume.  The volume
will be used to store the functions' source code.

Etcd is used as the DB.


Container Pool Manager 
----------------------

Manage pool of generic containers.

Probably use K8s RCs.  Can we Use labels to move pods from one rc to
another?  What about jobs?

Container Specializer
---------------------

Inputs: a running generic language run container, a user function,
optionally an http trigger URL.

Calls Language Run Container and sets up Router to point to it.


Router
------

- Cache trigger -> container instance mapping; implement cache miss and expiration.

- Invoke Specializer, setup up k8s API 

- Forward requests

The Router is stateless -- it can be scaled or killed at any time.

There's a lot of functionality overlap with K8S Ingress Controllers.
We should clearly use Ingress and Ingress Controllers in some way.
It's not exactly clear at the moment how -- should make a whole new
Ingress Controller perhaps based on the contrib/nginx


Autoscaler
----------

This autoscales the language run containers that are backing a
trigger.

What metrics this is based on is TBD.

- Number of requests/sec
- "Backlog" -- number of outstanding requests not yet started -- how to measure this?
- Change in turn-around time?


Language Build Container
------------------------

* The Language Build Container is a container that is invoked for a
  build.  It takes one user-created function and outputs something
  that can be run by the corresponding Language Run Container.

* The Build Container must implement the Language Build Container
  interface.


Language Run Container
----------------------

* The Language Run Container is the container in which user functions
  run.

* The Run Container is started without the user function.  It must
  start as a "Generic Container".  It must implement the
  "specialization interface".  In short, it must implement an HTTP
  server that can receive a piece of code, verify its signature, and
  map it to an HTTP endpoint.  See
  Documentation/specs/LanguageRunContainerSpec.md for details.



