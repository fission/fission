# Fission Environments Redesign

As Fission supports more languages and reaches a wider set of use
cases, it's time to ask how well the current Environments design is
holding up.


## Environments V1: What we learned

Environments V1 is very simple idea: an environments is one Docker
image with an HTTP server + dynamic loader for that language; it's run
in a pod with a language-agnostic sidecar (fetcher) that downloads and
saves the function into a volume shared with the language-specific
container.

### Pros:

* Abstracted away images.

* Very fast cold start

* No image registry to manage (neither for the user nor for fission
  implementation)

* Relatively small amount of language specific code. (python env is <
  100 lines)

### Cons:

* Doesn't work well for compiled languages

* Users have to rebuild the image to add dependencies

* Only one file supported

* Errors in loading are not surfaced properly.  It is especially
  annoying to wait until runtime to see a syntax error that could have
  been caught on function upload.

* Not great for a large code base

* Some people want to operate at the image level but still get the
  on-demand execution semantics of FaaS.  This is a cost-optimization
  use case.


### Discussion

Early feedback shows that almost evey user ends up rebuilding images
to add some dependecies.  Some sort of automated dependecy resolution
would be very nice to have and improve the development workflow.  In
other words, just attach a package.json(nodejs) or
requirements.txt(python) with a function, and fission will do the
rest.  There's also the possiblity of supporting buildpacks (simple
zipfiles), a la AWS Lambda.

Though we can support compiled languages by doing the compilation
inside the cold-start, that's not a great solution because: (a)
compile errors would be reported at runtime, and (b) because the
overhead of compilation doesn't really need to be inside the
cold-start latency.

Non-trivial functions will need multiple files.  That also helps for
common code across functions.  So we need a way for the user to define
a function as a collection of code with an entry point.

Finally, Docker images remain the most flexible way to package an app.
Today, users can always rebuild an environment image to include
anything they want.  But those images must still run a server that
implements fission-environment interface (i.e. the specialize
endpoint).  So perhaps there could be a way for users to say "don't
use environments, I've already packaged up my function, here it is".


## Environment V2 Requirements

Roughly in order of priority:

0. Retain the simplicity of the simple use cases.  First user
   experience shoud remain trivial -- write a function, map a URL,
   done.

1. Support compiled languages. Support error reporting on function
   upload rather than cold start.

2. Support functions as a collection of files rather than just one
   file.

3. Support automated environment-specific dependecy resolution.

   (#3 may end up having the same solution as #1.  You could think of
   gathering deps as a "compilation" of package.json,
   requirements.txt, etc.)

4. Support functions as images.


### User stories

#### Compiled language

User writes a function in Go.

```
      $ fission function create --code blah.go

      <compilation errors>

      <user edits file>
      $ $EDITOR blah.go 
      <fixes errors>

      $ fission function update --code blah.go
      <success>
```

(Or perhaps we could have a `fission function check --env x --code y`
which just does compilation, without creating a function object?
Useful for integration into IDEs.  Basically, just an on-demand
builder. Useful when you don't wanna setup anything on your laptop.)

This same user story applies to interpreted languages too, where the
"compilation" step can be used to check for syntax errors.


#### Collections of files

We should probably have a manifest in YAML/JSON/etc syntax for
specifying a function.  We could also use that YAML to let users
specify the function's environment, resource requirements, etc.

```
        $ fission funcion create -f blah.yaml

        $ cat blah.yaml
        type: Function
        metadata:
           name: ...
        environment: ...
        files:
        - foo.py
        - bar.py
        - baz/*.py              
```     

The yaml file could specify a list of files.  The fission client would
deal with packaging up this set of files and uploading the package.


#### Handling Dependencies

A manifest could point at a function's dependency spec.  The
Environment's "builder" container could then fetch these deps.

So a NodeJS function manifest could contain a reference to
package.json.  A "builder" container in the NodeJS environment would
then run npm on that function.

```     $ cat func.yaml
        type: Function
        metadata: ...
        environment: ...
        dependencies: package.json

        $ fission function create -f func.yaml
```

