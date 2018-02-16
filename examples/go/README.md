# Hello World in Go on Fission

`hello.go` contains a very simple fission function that says "Hello, World!".

## Deploying this function on your cluster

```bash

# Create the Fission Go environment and function, and wait for the
# function to build.  (Take a look at the YAML files in the specs
# directory for details about how the environment and function are
# specified.)

$ fission spec apply --wait
1 environment created
1 package created
1 function created

# Now, run the function with the "fission function test" command:

$ fission function test --name hello-go
Hello, World!
```
