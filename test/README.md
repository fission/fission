# Test Framework

## Prerequisite tools

- GNU parallel >= 20161222

If you want to run test on Mac, you also need to install GNU tools for command compatibility.
With Homebrew, you can get them by:

       brew install coreutils findutils gnu-sed


## Run a single test

Example: Run `test_node_hello_http.sh`.

```bash
    cd $REPO/test
    ./tests/test_node_hello_http.sh
```


## Run tests in parallel

NOTE: Some tests will consume lots of resources and cause the test fail.

Example: Run tests with 4 concurrent jobs.
```bash
cd $REPO/test

export JOBS=4
./run_test.sh \
    tests/test_backend_poolmgr.sh \
    tests/test_node_hello_http.sh \
    tests/test_pass.sh \
    tests/test_specs/test_spec.sh

# The test output will be available in logs/
cat logs/test_backend_poolmgr.sh.log
```

