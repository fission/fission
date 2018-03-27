## Upgrade tests

Currently in Fission, there is no way to run tests between upgrades and check if the upgrade broke something. This proposal aims to take a shot at this and while we are at it, a small diversion into related topic of testing! Please share your feedback and comments.

### Goals
- Newer version of Fission CLI should work with older version of Fission
- A newer version of Fission passes all tests from previous version of Fission (Regression)
- A cluster when upgraded from previous release behaves in the same way as when a newer version is installed from scratch on a cluster

### Non-goals
- Tests from newer version of Fission working with older versions of Fission

### Proposal

The upgrade test is run only once in a certain frequency (Mostly once a day) based on a cron job. Two version of fission source code are checked out in two subdirectories, the parameters for the version could be specified as environment variables.

```
./fission
./fission_old
```

1. Fission_old is built and deployed in the cluster and then tests are run. This is exact same as the current build and test workflow but for a previous version of Fission.

2. Once (1) passes, script switches to fission directory and builds and pushes the images. The part where the workflow diverts is install_and_test, and here are the differences:

- Instead of ```helm install```, we will run ```helm upgrade``` - and upgrade the existing cluster with newer images
- Script will use the new fission CLI
- Script will use tests in fission_old to run the tests

3. The script for (1) can be used from existing codebase while the part (2) will need developing a part of script specifically for upgrade.

4. After the run - if both set of tests succeed, the upgrade can be considered fairly successful (Although not entirely - as there can be potential gap)

### Alternative approach considered

It is possible to get the binary from previous release instead of source code - but it has two drawbacks:

1. The effort for (2) in previous section still remains the same
2. With binary install, we loose the tests from older version of Fission.

## A small Diversion

While researching for the best way to run upgrade tests, I was looking at testing frameworks used by other go projects. I found Ginkgo (A BDD framework) and Gomega  (A matcher library). The experessiveness of the tests and constructs for common issues such as retry-backoff etc. are well built and I found some aspects which are relevant to our tests. Details below:

### Issues & possible solution

- Most of our tests are sequential - but in real life there are multiple things going on a server and we should mimic that - i.e. run tests in parallel. This is also beneficial to reduce overall test time. (Of course there are exceptions)

Ginkgo allows tests to run in parallel and fires them in separate threads. It also allows randomizing the tests for every execution


- Run selective test suites with certain purpose, for ex. test all performance tests is very rudimentry  with shell scripts. Also it is difficult to specify some some actions which should be run "before executing test suite 1" and after finishing execution and so on.

Ginkgo provides constructs such as ```BeforeSuite``` and ```AfterSuite``` and the same for each test with ```BeforeEach`` and ```AfterEach```. This allows clear separation of fixtures from actual tests and also common setup and teardown

- Sometimes, some of our tests fail unexpectedly and pass on the next attempt. A lot of time is spent in debugging and fixing in PRs on those tests. While each test should be fixed eventually, it should be possible to retry a flaky test as part of execution.

- Any test which needs to absolutely disrupt the cluster should not be run in parallel by using some tag.

- Various runtime behaviours such as fail on first failure or keep testing till failure or randomize order of tests etc. are difficult to model with shell scripts.

- Ability to measure time for all tests without doing time manipulation in shell scripts. Ginkgo provides built in constructs to measure test suite time and each individual execution time.

### Next steps

The best way to know usefulness of the test framework would be to model some of existing tests using the framework and demonstrate the value. This PR will add more details with few of tests remodelled to use the framework.