Programming Model
=================

This document describes the programming model for fission functions.  

See TERMINOLOGY for definitions of terms, such as _function_,
_instance_, and _trigger_.


Idempotency
===========

Fission assumes that functions are idempotent.  Functions whose instances
die without a result are restarted, upto a certain restart limit.


Time Limits
===========

By default, there is no time limit on fission functions.  

Idle running instances may be killed at any time (usually after the
default idle timeout of 10 minutes, but this is configurable).


Mapping
=======

This section specifies the mapping between: (1) HTTP and other
triggers, and (2) function parameters, return values, and exceptions.

In other words, this section describes how a function should be called
based on a given trigger, and how the functions behaviour affects the
result returned from the trigger.

The HTTP Trigger
----------------

NodeJS
------

NodeJS functions are called with a context object.

context.request contains the nodeJS Request object.

In addition,

context.queryString contains the parsed querystring

context.body contains the parsed body

context.status sets the HTTP response status code.  If context.status
is an invalid HTTP status, then the HTTP status is set to 500.

Exceptions result in a HTTP 500 error.





XXX Should we have a "context.done()"?  What are the pros and cons?

XXX Who handles serialization and deserialization?  Should we do json
    automatically based on content-type and accept headers?


Python
------

Same as node, pretty much.

