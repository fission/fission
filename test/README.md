# Test Framework

## Prerequisite tools

- GNU parallel >= 20161222

If you want to run test on Mac, you also need to install GNU tools for command compatibility.
With Homebrew, you can get them by:

       brew install coreutils findutils gnu-sed


## Run a single test


```bash
cd $REPO/test

# run 'tests/test_node_hello_http.sh'
./tests/test_node_hello_http.sh

# if you want to preserve the resources that created by test for debug
export TEST_NOCLEANUP=yes
./tests/test_node_hello_http.sh

# run 'tests/test_environments/test_go_env.sh' with custom images
export GO_RUNTIME_IMAGE=my.docker.repo/go-env:test
./tests/test_environments/test_go_env.sh
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

# The summary report will be saved to logs/_recap
cat logs/_recap
```

