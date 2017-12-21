# Function Versioning

## Usecases for function versioning from POV of fission users 
1. Create a new version of a function with code changes ( perhaps adding some new features to a function )
2. Have two versions of a function deployed and perform canary testing by creating an HTTP trigger for both versions of a function.
3. Create an HTTP Trigger for a function with a specific version, if not fall back to the `“latest”` version of a function.
4. Create other kings of triggers to a function with a specific version.
5. Create a set of functions and group them as one version. ( perhaps run a group of functions as a cronjob with one timeTrigger, haven’t thought this through )


## Design approaches to satisfy above requirements

### Approach 1 

Introducing a new CRD of type `"Version"` to accommodate the above use cases effectively. The need for this is explained in point 2 under Code changes needed for usecase 1 and 2. Also, in point 1 under Code changes needed for usecase 4.
```
Type VersionSpec struct {
	FunctionRef []string
}
```


#### Usecase 1 and 2 

CLI changes :
1. Allow the user to create more than one function with same name by letting them pass a version number with a flag `"—version"`.

```bash
$ fission function create --name hello --env nodejs --code hello1.js --version v1.0.0
$ fission function create --name hello --env nodejs --code hello2.js --version v1.0.1
$ fission function create --name hello-nv --env nodejs --code hello3.js —> this falls back to non-versioned functions. So if there is another request to create a function with the same name, it will fail
```


Code changes :

1. In fnCreate : Just ligitke today, we first create a `crd.package` object. Next step is to create a `crd.function` object for each version of the function. The name of the `crd.function` object can be `"functionName_versionNumber"`.  
2. Next, create an object of type `crd.Version` to keep track of the `"latest"` version of a function. The name of this object can be something like "functionName_versionLatest" and the value of the field `FunctionRef` will be an array of function names such as `["hello_v1.0.0", "hello_v1.0.1"]`. So at any point in time, for a function its easy to get the function object that is latest.


#### Usecase 3 

CLI changes :
1. Allow the user to create a http trigger for a specific version of a function. Also fall back to the `"latest"` version if version is NOT passed by the user while creating the trigger.

```bash		
$ fission route create --method GET --url /hello --function hello --version v1
$ fission route create --method GET --url /hello --function hello
```

Code changes :
1. Add a new type of functionRef (as per comments in `types.go`) called `"FunctionReferenceTypeFunctionVersion"` 
2. Create the trigger object with this functionRef spec. 
3. Make changes to `resolve` function in `"functionReferenceResolver.go"` to handle this new type and pass in the metadata of the specific version of a function.
( At this point, it may seem like the new type is not necessary because we have different function object names for different versions of the function. But I still think it keeps the code cleaner and easier to understand and maintain )

#### Usecase 4 
Allow the user to create other types of triggers for a specific version of a function. Also fall back to `"latest"` version if not passed by the user while creating the trigger.

Code changes :
1. Support the new functionReference Type in all of the triggers respectively.

#### Usecase 5 

CLI changes :
1. Allow the user to create a version group and tag a set of functions with the same version group.

```bash
$ fission function create --name foo --env nodejs --code foo.js —-tag v1.0.0
$ fission function create --name bar --env nodejs --code bar.js —-tag v1.0.1
```

Code changes :
1. Create an object of type `crd.Version` with the name supplied as tag value in the above CLI. The value of the field `FunctionRef` will be an array of function names created with this tag.
2. Haven't still figured out the code for TimeTrigger yet. But will fill in the code changes required there to handle this later.

#### Note :
1. With this proposal, the syntax of versions is left to the fission users and fission code doesn't handle them differently. Ex : the user can create functions with versions such as v1, v2 so on. Or, Vx.y.z following the semantics of semver.
Do let me know if you think these need to be handled differently.
2. With this proposal, the `"latest"` version of a function will point to the most recent version created for that function. Referring to CLI example under use case 1 and 2, the function hello will have its "latest" pointed to v1.0.1.
There might be a gotcha over here that needs explicit stating. If a user creates a function version 1.0.1 first and then creates a function version 1.0.0, the "latest" will point to v1.0.0. I feel like this is an anti pattern and this will/should never happen. But would like to hear your opinion.
3. The thing I haven't considered yet is allowing the user to overwrite a function version. Do we need to?


### Approach 2

I have another approach in mind. Haven't yet figured out how to fit in all use cases with this approach and preserve backward compatibility.
I'll probably add details of it once I have it all figured out.











