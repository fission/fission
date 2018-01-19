# Hello World in Go on Fission

`hello.go` is an very simple fission function that says "Hello, World!".

```bash
# This command creates the environment and function, and waits for the
# function build.  Look at the YAML files in specs/ for details about
# how those are specified.
$ fission spec apply --wait
1 environment created
1 package created
1 function created

# This run the function and prints its output
$ fission function test --name hello
Hello, World!
```
