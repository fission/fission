# Fission Terminology

_Function_: A fission function is something that's mapped to a
_trigger_ and run on demand.  Though we call it a "function", this is
a bit imprecise, since it's actually a module with an function as an
entry point -- it doesn't have to be just one function.

_Trigger_: Triggers are what cause functions to be called.  For
example, an HTTP trigger causes functions to be called on HTTP
requests.  Kubernetes Watch triggers cause functions to be called when
a Kubernetes watch changes.  Future triggers will include message
queues, timers, storage systems, etc.

_Environments_: Environments are the language-specific parts of
Fission.  Environment containers wrap the user's function and present
a common interface to the rest of the fission framework.

