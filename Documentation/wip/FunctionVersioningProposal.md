# Function Versioning

## Use cases for function versioning from POV of fission users :
1. Create different versions of a function with code changes.
2. List all versions created for a function.
3. Delete a specific version of a function. (Note : Not allowing entire CRUD on a version of a function. Does update of a function version make any sense? )

4. Create aliases to different function versions. Ex : `"Dev"` referring to `"V3"` of a function, `"Qa"` referring to `"V2"` of a function, `"Prod"` referring to `"V1"` of a function. By default `"latest"` referring to most recent function version created.
5. List all aliases created for a function.
6. Update an alias to point to a different version of a function.
7. Delete a function alias. (This shouldn't delete the function object that this alias refers to, instead just delete the alias object)

8. Delete a function by name, which will then cascade deletion of all aliases and all versions of that particular function.

9. Create an HTTP Trigger for a function with a specific version.
10. Have two versions of a function deployed and perform canary testing by creating an HTTP trigger for both versions of a function. // TODO : Come back to this
11. Create other kinds of triggers to a function with a specific version.
12. Create a group of functions and tag them as one version. ( perhaps run this group of functions as cron jobs with one timeTrigger, haven’t thought this through ) // TODO : Come back to this



### Design approach :
#### Data Structures
Few new data structures for this approach are listed below. The need for these become evident once you go through the GeneralFlow section

* Introducing a new CRD of type `"Alias"`. This will refer to a specific version of a function.
```
Type Alias struct {
	Name string
	FunctionRef FunctionRef
} 
```
* Introducing a new CRD of type `"FunctionMaster"`. This will have a reference to all aliases of a function. Also will have a list of references to all versions created for this function.
```
Type FunctionMaster struct {
	Name string
	Alias []Alias
	Versions []FunctionRef
}
```

* Introducing a new CRD of type `"Tag"`. This is to support use case 12 listed above.
```
Type Tag struct {
	Name string
	FunctionRef []FunctionRef
}
```

#### General Flow 
1. When a user wants to create a function with different versions, he can do so with the following commands
    ```bash
    $ fission function version create --name hello --env nodejs --code hello1.js --version v1.0.0
    $ fission function version create --name hello --env nodejs --code hello2.js --version v1.0.1
    $ fission function version create --name hello --env nodejs --code hello3.js --version v1.0.2
    ```
   For each version of a function, an object of type `crd.function` will be created with name "functionName_version". 
   For this example, 3 objects of type `crd.Function` will be created with names `"hello_v1.0.0"`, `"hello_v1.0.1"` and `"hello_v1.0.2"`. The other fields of the `crd.Function` structure are filled as-is.

2. Next, an object of type `crd.Alias` will be created with name `"functionName_latest"`. For the above example, an alias object is created with name `"hello_latest"` and FunctionRef pointing to `"hello_v1.0.2`. Note: `"latest"` will always point to most recent version of the function created.

3. Next, an object of type `crd.FunctionMaster` is created with name `"functionName_master`. This data structure is needed to hold all aliases created for a function and all versions created for a function. For the above example, an object `"hello_master"` is created with `"Alias"` field referring to `"hello_latest"` and the `"Versions"` field referring to a list with `"hello_v1.0.0"`, `"hello_v1.0.1"` and `"hello_v1.0.2"`. ( Feel free to suggest a better name for this data structure )

4. Next, if the user wants to `list` all versions of a particular function, he can issue the following command
    ```bash
    $ fission function version list --name hello 
    ```
   The backend will first retrieve the `crd.FunctionMaster` object for this function and fetch all function versions referenced in the functionMaster object.

5. Now, lets say the user wants to create an alias called `"qa"` for version `"v1.0.1"` in the above example, he can issue the following command
    ```bash
    $ fission function alias create --name hello --alias qa --version v1.0.1
    ``` 
   The backend will first retrieve the functionMaster object and check that indeed this version exists. Then, it will go ahead and create an object of type `crd.Alias` with name `"functionName_aliasName"`, referring to the above example : `"hello_qa"` and FunctionRef pointing to `"hello_v1.0.1"` function object. The backend will then update the functionMaster object to have this new alias appended to `"Alias"` field.

6. Next, if the user wants to list all the aliases created for a particular function, he can issue the following command :
    ```bash
    $ fission function alias list --name hello
    ```
   The backend will fetch the functionMaster object and list all the aliases from the `"Alias"` field.

7. Next, if the user wants to update the alias `"qa"` to a different version of the function, he can do so with the following command
    ```bash
    $ fission function alias update --name hello --alias qa --version 1.0.2
    ```
   At this point, the backend will update the `"hello_qa"` alias object to refer to function object `"hello_v1.0.2"`. Note at this point that both aliases "latest" and "qa" point to the same version of the function. This is completely fine.

8. If the user wants to delete a particular alias, he can do so with the following command
    ```bash
    $ fission function alias delete --name hello --alias qa
    ```
   The backend will delete the alias object `"hello_qa"`. Note that the function objects are not deleted. The backend will also update the functionMaster object's "Alias" field and remove the deleted alias object from the list.

9. If the user wants to delete a particular version of a function, he can do so with the following command
    ```bash
    $ fission function version delete --name hello --version v1.0.0
    ```
   At this point, the backend will first retrieve the functionMaster object and check if there is any alias mapped to this version. If yes, it errors out with a message to the user that the version is in use by an alias. If not, it will go ahead and delete the functionObject `"hello_v1.0.0"` and update the `"Versions"` field by deleting the reference of that version.

10. If the user wants to create a http trigger for a specific version of a function, he can do so with the following command
    ```bash		
    $ fission route create --method GET --url /hello --function hello --version v1.0.2
    ```
    The backend needs a new type of functionRef (as per comments in `types.go`) called `"FunctionReferenceTypeFunctionVersion"`. Also, changes to the `resolve` function in `"functionReferenceResolver.go"` are needed to handle this new type.
    First, it creates the trigger object with this functionRef spec. Next, it passes the metadata of the specific version of a function in `resolve` function.
    (It does seem like the new type is not necessary because we have different function object names for different versions of the function. But I still think it keeps the code cleaner and easier to understand)
    For canary testing, we may need to accept different percentage values as an input to re-direct traffic accordingly to different versions. This needs more discussion.

11. The user may want to create other types of triggers for a specific version of a function. 
    For this, the backend will need to support the new functionReference Type in all of the triggers respectively.

12. Allow the user to create a version group and tag a set of functions with the same version group.
    ```bash
    $ fission function create --name foo --env nodejs --code foo.js —-tag v1.0.0
    $ fission function create --name bar --env nodejs --code bar.js —-tag v1.0.0
    ```
    First and object of type `crd.Tag` with the name supplied as tag value in the above CLI is created. The value of the field `FunctionRef` will be an array of function names created with this tag.
    (Haven't still figured out the code for TimeTrigger yet. But will fill in the code changes required to handle this later)

13. If the user wants to delete a function by name, he could do so with the existing CLI. But the code will need to delete all alias objects and all function version objects created for this function.


#### Note 
1. With this proposal, the syntax of versions is left to the fission users and fission code doesn't handle them differently. Ex : the user can create functions with versions such as v1, v2 so on. Or, Vx.y.z following the semantics of semver.
Do let me know if you think these need to be handled differently.
2. With this proposal, the `"latest"` version of a function will point to the most recent version created for that function. 
There might be a gotcha over here that needs explicit stating. If a user creates a function version 1.0.1 first and then creates a function version 1.0.0, the "latest" will point to v1.0.0. I feel like this is an anti pattern and this will/should never happen. But would like to hear your opinion.

