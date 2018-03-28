## Upgrade tests

Currently in Fission, there is no automated way to run tests between upgrades and check if the upgrade broke something.

### Goals
- Objects created and tests executed in a version of Fission should continue to work in the upgraded version

### Proposal

The upgrade test is run only once in a certain frequency (Mostly once a day) based on a cron job.

For clarity we will refer to old version as fission_old and the version after upgrade as fission_upgraded

#### Iteration 1

1. Fission_old is deployed using the binary version from Github.

2. A script creates a function and tests it. It then outputs some metadata in a temporary file. There are two kinds of metadata:
- One is objects that were created
- Second is routes that were tested

Object metadata:
```
function|hellopy
environment|hello
```

Route metadata:
```
/hellopy
/hellojs
```

3. After that upgrade is done and another script runs tests again and cleans up objects using metadata file.

So a rough model evolves at this point:

Pre Upgrade: Setup, test
Post Upgrade: Test, cleanup

#### Iteration 2

In previous iterarion we had to write tests specifically for upgrade - but what if we could use all the tests normally written also during upgrade - that will make the upgrade testing more comprehensive.

But to fit in model above, we will have to have three parts clearly separated for any test:

- Setup
- Test
- Cleanup

And ability to pass metadata between the parts. That would mean getting away from all `TRAP` statements and having a proper demarkation of steps.

Also the metadata needs to be enhanced to associate objects created by certain tests:

```
test1|function|hellopy
test2|environment|hello
```

#### Limitations

1. The above method is great for simple http requests but many a times a test is updating a function and then testing with a simple http request. While it is still possible to model this - it can get complex quite fast.

2. A test from x.y+1 release may not work with x.y release.