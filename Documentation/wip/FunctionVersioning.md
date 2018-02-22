Table of Contents :

* [Use cases of creating versions of functions and packages](#use-cases-of-creating-versions-of-functions-and-packages)
* [High level proposal](#high-level-proposal)
* [Implementation details](#implementation-details)
  * [Implementation details for fission spec](#implementation-details-for-fission-spec)
     * [TL;DR of intended behavior of fission code through different scenarios](#tldr-of-intended-behavior-of-fission-code-through-different-scenarios)
        * [Version creation](#version-creation)
        * [Version deletion](#version-deletion)
     * [Pseudocode for spec](#pseudocode-for-spec)
  * [Implementation details for fission cli commands](#implementation-details-for-fission-cli-commands)
     * [TL;DR walking you through various scenarios](#tldr-walking-you-through-various-scenarios)
        * [Version create for packages](#version-create-for-packages)
        * [Version create for function](#version-create-for-function)
        * [Version update for packages](#version-update-for-packages)
        * [Version update for functions](#version-update-for-functions)
        * [Version delete for packages](#version-delete-for-packages)
        * [Version delete for functions](#version-delete-for-functions)
        * [Version list for packages](#version-list-for-packages)
        * [Version list for functions](#version-list-for-functions)
     * [Pseudocode](#pseudocode)
* [Note](#note)
* [Conclusion](#conclusion)
   
# Use cases of creating versions of functions and packages

1. A user may want to save a history of all versions of source code deployed for a function, basically leaving an audit trail
2. A user may want to test different versions of his function ( canary testing )
3. A user may want to do a rolling upgrade from one version to another version of his function.
4. A user may want to submit his source code changes to git and then just invoke `fission spec version apply`, that'll take care of creating .
5. A cluster administer may want a record of the exact versions of different functions deployed at any given point in time ( for troubleshooting some issues in the cluster and making them reproducible at a later point perhaps)

# High level proposal
1. This proposal intends to create a new function version every time the user does a `function version update` - basically creating a new function object.
   Implicitly, this means that fission takes care of assigning versions. 
2. The version numbers are not sequentially increasing numbers, instead, they are randomly generated uuid short strings. The reason for this is explained under "Implementation changes needed for fission spec".
3. So, every new function version will be a function object with name function name and a version uuid suffix
   For example - lets say a user first creates a function "fnA" with `fission function version create --name fnA` and then he updates this function with `fission fn version update --name fnA --code <new_code>`, a new function object is created with `Name: fnA-axv`.
   Next, when he updates the same function again, another new version of a function is created, ex : `Name: fnA-reh`.
3. This proposal also intends to version packages along with functions. This is because fundamentally, function versioning implies that the source code is versioned and in fission, packages contain source code. 
4. Naturally, the intention is to version packages similar to functions. So, every pkg update results in a new pkg object being created, which is treated as a version of that package.

# Implementation details
Classifying the implementation details broadly under the following two categories because these are the only ways function and package objects along with other fission objects can be created as of today.
1. implementation needed W.R.T `fission spec` to automatically deploy all the desired objects from the spec into k8s cluster. 
2. implementation needed W.R.T fission cli commands where users individually create the desired objects.

Although I've tried to keep the behavior of the code consistent and similar between the above 2 categories, there are a few variations and they become evident as you go through the various scenarios detailed below.
I've also summarized the differences in the end.

Before diving into scenarios, here's a list of a few data structures needed :
This proposal intends to create 3 new object types for managing the state - `FunctionVersionTracker`, `PackageVersionTracker` and `GroupVersionTracker`.

1. FunctionVersionTracker -  of type `FunctionVersionTracker` is used to track all versions created for each function. 
   This object gets created when the first version of the function is created. The name of the object will be the function name and the version uid will be appended to the `"versions"` list in the `"spec"`. Also, whenever a new version of the same function is created, the version uid will be appended to the list.

    ```go
        type FunctionVersionTracker struct {
            metav1.TypeMeta `json:",inline"`
            Metadata        metav1.ObjectMeta   `json:"metadata"`
            Spec            FunctionVersionTrackerSpec `json:"spec"`
        }
        type FunctionVersionTrackerSpec struct {
            Versions []string `json:"versions"`
        }
    ```

2. PackageVersionTracker - of type `PackageVersionTracker` is used to track all versions created for each package. The usage of this object is similar to that of `FunctionVersionTracker`. Every new version of the package object created gets appened to the `"versions"` list in the spec. 
    ```go
        type PackageVersionTracker struct {
            metav1.TypeMeta `json:",inline"`
            Metadata        metav1.ObjectMeta   `json:"metadata"`
            Spec            PackageVersionTrackerSpec `json:"spec"`
        }
        type PackageVersionTrackerSpec struct {
            Versions []string `json:"versions"`
        }
    ```

3. GroupVersionTracker - used to track all versioned objects created in each iteration of `spec version apply`. It also has some metadata about git status, branch etc. This can give the state of k8s cluster at that point in time, necessary for use-case 5 mentioned above. The usage of this object becomes evident when you look at scenario 1 below.
    ```go
        type GroupVersionTracker struct {
            metav1.TypeMeta `json:",inline"`
            Metadata        metav1.ObjectMeta   `json:"metadata"`
            Spec            VersionTrackerSpec `json:"spec"`
        }
        type GroupVersionTrackerSpec struct {
            gitInfo GitInfo 
            objects []VersionObject
        }
        type GitInfo struct {
            Commit string
            Branch string
            Clean bool
        }
        type VersionObject struct {
            Type string
            Name string
            Version string
        }
    ```

4. Introducing a new annotation for all versioned functions in the function object : `"versioned" : "true"`.

5. Introducing a new annotation for all versioned packages in the function object : `"versioned" : "true"`.


## Implementation details for `fission spec`
Dividing this section further into 
1. TL;DR walking you through various scenarios and how the code intends to behave and the snapshot of objects created etc. 
2. Pseudocode

### TL;DR of intended behavior of fission code through different scenarios
The list of scenarios are definitely not all-encompassing. but at a minimum, it's an attempt to show the generic behavior of versioning code.
Summary of the scenarios (CRUD) :
scenarios 1 - 4 touch upon creation of different versions of functions and packages.
scenario 5 touches upon deletion of versions of function and packages.
There is no concept of updating versions - every new update on a function or a package results in a new version.
Listing all versions of a function and/or package is possible and that's covered under "Implementation details for CLI"

#### Version creation
1. For this scenario, let's assume there's nothing on the kubernetes cluster except that fission is installed and its micro-services are running.
   So, lets say the user wants to deploy a `"hello"` function for the first time on to the k8s cluster, he creates the source code in `"hello.py"` object, makes a spec `hello.yaml` as below, commits his code and invokes a `spec version apply`.
   user's yaml contains the following :
   ```yaml
   apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: hello
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: hello-pkg
          namespace: default
        functionName: hello.main
    
    ---
    apiVersion: fission.io/v1
    kind: Package
    metadata:
      name: hello-pkg
      namespace: default
    spec:
      source:
        url: archive://hello-archive
      buildcmd: "./build.sh"
      environment:
        name: python-27
        namespace: default
    status:
      buildstatus: pending
    
    ---
    kind: ArchiveUploadSpec
    name: hello-archive
    include:
      - "hello/*.py"
      - "hello/*.sh"
      - "hello/requirements.txt"
   ```
   here's what will happen in the code :
   
   1. A function object is created with the given name suffixed by a version uid, for example `hello-owf` and a package object with name `hello-pkg-owf`, for example, will get created on k8s cluster.
      in addition, objects of type `PackageVersionTracker` and `FunctionVersionTracker` are created. here are the object snapshots :
      ```json
      {
            "kind": "Function",
            "metadata": {
                "name": "hello-owf",
                "annotations": {
                  "versioned" : true
                }
            },
            "spec": {
                "InvokeStrategy": {
                    "ExecutionStrategy": {
                        "ExecutorType": "poolmgr",
                        "MaxScale": 1,
                        "MinScale": 0,
                        "TargetCPUPercent": 80
                    },
                    "StrategyType": "execution"
                },
                "configmaps": [],
                "environment": {
                    "name": "python-27",
                    "namespace": "default"
                },
                "package": {
                    "packageref": {
                        "name": "hello-pkg-owf",
                        "namespace": "default",
                        "resourceversion": "101877"
                    }
                },
                "resources": {},
                "secrets": []
            }
      },
      { 
            "kind": "Package",
            "metadata": {
                "name": "hello-pkg-owf",
                "resourceversion": "101877",
                "annotations": {
                  "versioned" : true
                }
            },
            "spec": {
                "source": {
                    "checksum": {},
                    "literal": "<some_literal>",
                    "type": "literal"
                },
                "environment": {
                    "name": "python-27",
                    "namespace": "default"
                },
                "deployment": {
                    "checksum": {}
                }
            },
            "status": {
                "buildstatus": "pending"
            }
      },
      {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "hello"
            },
            "spec": {
                "versions": ["owf"]
            } 
      },
      {
            "kind": "PackageVersionTracker",
            "metadata": {
                "name": "hello-pkg"
            },
            "spec": {
                "versions": ["owf"]
            } 
      }
      ```
      Also, an object of type `GroupVersionTracker` gets created. This basically has data about all versioned objects that are present on the cluster at the end of this iteration of `spec version apply`. In addition to that, it saves some metadata about the user's git commit. This can help the user at a later point if he has to troubleshoot any issue etc.
      For every invocation of `spec version apply`, the code will generate a unique version suffix which will be used as version suffixes for functions and packages. Also used for the name of this object.
      currently, we are versioning only packages and functions. in the future, if any other object is versioned, it can also be seamlessly added into the list of objects. 
      Here's the snapshot of that object :
      ```json
      {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "owf"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "hello",
                       "version": "owf" 
                     },
                     {
                       "type": "package",
                       "name": "hello-pkg",
                       "version": "owf" 
                     }
                ]  
            } 
      }
      ```
   2. next, let's say the user changes the source code in one of the python files under "hello" directory and does a git commit, followed by a `fission spec version apply`.
   3. now the fission code creates a new archive object (this is not versioned); it sees that a package with the same name already exists (by looking at the `PackageVersionTracker` object), at which point, it compares the latest version of the package in the cluster with the package object in the user's spec and decides to version the package by creating a new package object with the same name suffixed by a version uid, ex: `hello-pkg-aub`.
   ( note : package name from user's spec cant be used anymore (like in `spec apply`) to find out if it is already present on the k8s cluster. instead, the `PackageVersionTracker` object is used. More details on exact comparison of objects can be found under Pseudocode section)
   ```json
       { 
            "kind": "Package",
            "metadata": {
                "name": "hello-pkg-aub",
                "resourceversion": "101879",
                "annotations": {
                  "versioned" : true
                }
            },
            "spec": {
                "source": {
                    "checksum": {},
                    "literal": "Cm1vZHVsZS5leHBvcnRzID0gYXN5bmMgZnVuY3Rpb24oY29udGV4dCkgewogICAgcmV0dXJuIHsKICAgICAgICBzdGF0dXM6IDIwMCwKICAgICAgICBib2R5OiAiSGVsbG8sIHdvcmxkIVxuIgogICAgfTsKfQo=",
                    "type": "literal"
                },
                "environment": {
                    "name": "python-27",
                    "namespace": "default"
                },
                "deployment": {
                    "checksum": {}
                }
            },
            "status": {
                "buildstatus": "pending"
            }
       }
   ```
   4. the fission code also appends `versions` list in the `PackageVersionTracker` for this package `"hello-pkg"` with the newly created version uid :
   ```json
       {
            "kind": "PackageVersionTracker",
            "metadata": {
                "name": "hello-pkg"
            },
            "spec": {
                "versions": ["owf", "aub"]
            } 
       } 
   ```
   5. fission code will next see that a function with the same name already exists (from `FunctionVersionTracker` object) and compares the latest version of function object in the cluster with that in user's spec. it then decides to version the function by creating a new function object with name `"hello-aub"`.
   (note : in order to find out if function object in the cluster is different from that in user's spec, few fields (example PackageRef) have to be compared differently. This because packages are versioned too and just comparing names will not work. Details under pseudocode section.)
   ```json
       {
            "kind": "Function",
            "metadata": {
                "name": "hello-aub",
                "annotations": {
                  "versioned" : true
                }
            },
            "spec": {
                "InvokeStrategy": {
                    "ExecutionStrategy": {
                        "ExecutorType": "poolmgr",
                        "MaxScale": 1,
                        "MinScale": 0,
                        "TargetCPUPercent": 80
                    },
                    "StrategyType": "execution"
                },
                "configmaps": [],
                "environment": {
                    "name": "python-27",
                    "namespace": "default"
                },
                "package": {
                    "packageref": {
                        "name": "hello-pkg-aub",
                        "namespace": "default",
                        "resourceversion": "101879"
                    }
                },
                "resources": {},
                "secrets": []
            }
       },

   ```
   6. next, it also updates the versions list of `FunctionVersionTracker` for this function `"hello"` with the newly created version uid :
   ```json
       {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "hello"
            },
            "spec": {
                "versions": ["owf", "aub"]
            } 
       } 
   ```
   7. the fission code will also create an object of type `GroupVersionTracker`. 
   ```json
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "aub"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "hello",
                       "version": "aub" 
                     },
                     {
                       "type": "package",
                       "name": "hello-pkg",
                       "version": "aub"
                     }
                ]  
            } 
       } 
   ```
   ( this might be the right point to explain why versions are not sequential numbers. let's assume the same scenario as above where the user's code and spec are checked into a git repo and many users concurrently create branches and work on their stuff.
   at any point in time the `spec version apply` might be called from anyone's branch. if we assigned sequential numbers, the numbers may not end up being sequentially increasing for each branch and hence might be confusing to users. so the ordering of versions is not maintained with numbers)
   
   
2. This scenario is similar to the first one, except we have an http trigger object in the spec for function `"hello""`. again starting with an empty k8s cluster with fission installed and the following yaml.
   This yaml is similar to scenario 1, only includes an http trigger object. But, I decided to make this a new scenario because resolving function reference by version needs a little explanation 
   ```yaml
   apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: hello
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: hello-pkg
          namespace: default
        functionName: hello.main
    
    ---
    apiVersion: fission.io/v1
    kind: Package
    metadata:
      name: hello-pkg
      namespace: default
    spec:
      source:
        url: archive://hello-archive
      buildcmd: "./build.sh"
      environment:
        name: python-27
        namespace: default
    status:
      buildstatus: pending
    
    ---
    kind: ArchiveUploadSpec
    name: hello-archive
    include:
      - "hello/*.py"
      - "hello/*.sh"
      - "hello/requirements.txt"
    ---
    apiVersion: fission.io/v1
    kind: HTTPTrigger
    metadata:
      name: d2aa2980-71d5-40b4-a490-4969646cbf7f
      namespace: default
    spec:
      functionref:
        name: hello
        type: name
        version: latest
      host: ""
      method: GET
      relativeurl: /hello
    
   ``` 
   Now, lets say, the user does a `fission spec version apply`, here's what will happen in the code :
   
   1. a function object with name `hello-uid`, a package object with name `hello-pkg-uid` will get created on k8s cluster. also, an object of httptrigger is created with the given spec. notice that the `functionRef` in the trigger has a `"version"` feild pointing to `"latest"` version of the function.
      This means that when the triggers are being resolved into the right function, it needs to resolve into the most recently created version of the function object. now, to find the most recently created version of the function, we use `FunctionVersionTracker` object which has a list of versions created for a function.
      Since we use a list, we automatically get the ordering, meaning, the last element in the list is the most recently created version of the function.
   2. now, lets say the user modifies his source code in one of the py files and invokes a spec version apply. Like explained in scenario, 2 function versions (`"hello-uid"`, `"hello-aub"`) and 2 package ( `"hello-pkg-uid"`, `"hello-pkg-aub"`) versions get created. however, there's no change to http trigger object. but because the version of the function it's pointing to is `"latest""`, at the time of function resolution, the trigger will be mapped to the `"hello-aub"` function object.
   3. there may be a scenario where the user deletes a function version that was created most recently. in other words, the function object that had been resolved as `"latest"` will now need to point to the one created before that. so, the plan is have a functionVersionController that does a listWatch of `FunctionVersionTracker` object and constantly syncsTriggers to invalidate stale entries from router cache.
   (note : please look at the code here for details on above explanation ( TODO : paste a link to my commit ) : that should give you a rough idea of how the resolution will take place and a little about the `FunctionVersionController`)
   
3. This scenario again starts with an empty k8s cluster with fission installed. While the user's yaml is similar to scenario 1, it has an additional function, package and archive. so basically showing the behavior of versioning when there are two functions with no shared packages.     
   ```yaml
   apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: foo
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: foo-pkg
          namespace: default
        functionName: foo.main
    
    ---
    apiVersion: fission.io/v1
    kind: Package
    metadata:
      name: foo-pkg
      namespace: default
    spec:
      source:
        url: archive://foo-archive
      buildcmd: "./build.sh"
      environment:
        name: python-27
        namespace: default
    status:
      buildstatus: pending
    
    ---
    kind: ArchiveUploadSpec
    name: foo-archive
    include:
      - "foo/*.py"
      - "foo/*.sh"
      - "foo/requirements.txt"
    ---
    apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: bar
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: bar-pkg
          namespace: default
        functionName: bar.main
    
    ---
    apiVersion: fission.io/v1
    kind: Package
    metadata:
      name: bar-pkg
      namespace: default
    spec:
      source:
        url: archive://bar-archive
      buildcmd: "./build.sh"
      environment:
        name: python-27
        namespace: default
    status:
      buildstatus: pending
    
    ---
    kind: ArchiveUploadSpec
    name: bar-archive
    include:
      - "bar/*.py"
      - "bar/*.sh"
      - "bar/requirements.txt"
    
   ``` 
   Now, lets say, the user does a `fission spec version apply`, here's what will happen in the code :
   
   1. two function objects with name `"foo-yti"`, `"bar-yti"`, two package objects with names `"foo-pkg-yti"`, `"bar-pkg-yti"` will get created on k8s cluster along with the `FunctionVersionTracker`, `PackageVersionTracker` and `GroupVersionTracker` objects accordingly.
   2. Now, lets say user updates the source code of only one of the functions, `"foo"` and invokes a `spec version apply`, fission code will see that package content varies only for `"foo"` and versions the function and package of `"foo"`.
   3. Here's a snap shot of all the objects in k8s cluster at this point :
   ```json
       {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "foo"
            },
            "spec": {
                "versions": ["yti", "abc"]
            } 
       },
       {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "bar"
            },
            "spec": {
                "versions": ["yti"]
            } 
       },
       {
            "kind": "PackageVersionTracker",
            "metadata": {
                "name": "foo-pkg"
            },
            "spec": {
                "versions": ["yti", "abc"]
            } 
       },
       {
            "kind": "PackageVersionTracker",
            "metadata": {
                "name": "bar-pkg"
            },
            "spec": {
                "versions": ["yti"]
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "yti"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "foo",
                       "version": "yti"
                     },
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "yti"
                     },
                     {
                       "type": "package",
                       "name": "foo-pkg",
                       "version": "yti" 
                     },
                     {
                       "type": "package",
                       "name": "bar-pkg",
                       "version": "yti" 
                     }
                ]  
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "abc"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "foo",
                       "version": "abc"
                     },
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "yti"
                     },
                     {
                       "type": "package",
                       "name": "foo-pkg",
                       "version": "abc" 
                     },
                     {
                       "type": "package",
                       "name": "bar-pkg",
                       "version": "yti" 
                     }
                ]  
            } 
       }
   ```
   please note that the `GroupVersionTracker` object that gets created during the 2nd iteration of `spec version apply`, refers to older version of function and package for `"bar"`. this will be filled using `PackageVersionTracker` and `FunctionVersionTracker` objects to find out their `"latest"` versions. 

4. The last scenario in version creation is one where there are 2 functions that share the same package, i.e. have the source code in one package for both functions. again, beginning with a fission installed k8s cluster with a clean slate. 
   user's yaml:
   ```yaml
   apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: foo
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: src-pkg
          namespace: default
        functionName: foo.main
    
    ---
    apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: bar
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: src-pkg
          namespace: default
        functionName: bar.main
    
    ---
    apiVersion: fission.io/v1
    kind: Package
    metadata:
      name: src-pkg
      namespace: default
    spec:
      source:
        url: archive://src-archive
      buildcmd: "./build.sh"
      environment:
        name: python-27
        namespace: default
    status:
      buildstatus: pending
    
    ---
    kind: ArchiveUploadSpec
    name: src-archive
    include:
      - "src/*.py"
      - "src/*.sh"
      - "src/requirements.txt"
    
   ``` 
   Now, lets say, the user does a `fission spec version apply`, here's what will happen in the code :
   
   1. two function objects with name `"foo-uid"`, `"bar-uid"`, one package object with names `"src-pkg-uid"` will get created on k8s cluster along with the `FunctionVersionTracker`, `PackageVersionTracker` and `GroupVersionTracker` objects accordingly.
   2. Now, lets say user updates the function code of only one of the functions, `"foo"` and invokes a `spec version apply`, fission code will see that they share a package and hence version the package first and also version both functions. This might seem a little counter-intuitive, but its consistent with how `spec apply` would work.
      For details on how this is possible, please refer to pseudocode section.
      Also, logically thinking, one of the reasons two functions may share a package is may be because one function talks to the other and they might have inter-dependent code. so, if one function's source is getting updated, there's a high chance the other function needs to be updated as well.
   3. Here's a snap shot of all the objects in k8s cluster at this point :
   ```json
       {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "foo"
            },
            "spec": {
                "versions": ["uid", "bla"]
            } 
       },
       {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "bar"
            },
            "spec": {
                "versions": ["uid", "bla"]
            } 
       },
       {
            "kind": "PackageVersionTracker",
            "metadata": {
                "name": "src-pkg"
            },
            "spec": {
                "versions": ["uid", "bla"]
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "uid"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "foo",
                       "version": "uid"
                     },
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "uid"
                     },
                     {
                       "type": "package",
                       "name": "foo-pkg",
                       "version": "uid" 
                     }
                ]  
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "bla"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "foo",
                       "version": "bla"
                     },
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "bla"
                     },
                     {
                       "type": "package",
                       "name": "src-pkg",
                       "version": "bla" 
                     }
                ]  
            } 
       }
   ```
   
 
#### Version deletion
Basically, with `spec`, its impossible to delete a specific version of a function or a package. This is because `spec` is designed to work like `kubectl apply`. So, the way an object in spec gets deleted is if its present in the spec at first and then removed from the spec, at which point, it's deleted.

Similarly, for versioned functions, if a user's yaml has a function in a particular iteration and it's missing in the next, then all versions created for that function will get deleted as part of the next `spec version apply` iteration.
Same applies to the packages. 

But fission cli can still be used to delete a particular version of a function. The spec stuff will work seamlessly even with deleted function versions or package versions.

Describing this explanation with a scenario :
5. For this scenario, let's continue from the state of k8s cluster at the end of scenario 4. To quickly summarize, the user's yaml had two functions, one package and one archive. The k8s cluster has two versions of functions `"foo"` and `"bar"` and two versions of package `"src-pkg"`.
   Now, if the user removes function foo from the users.yaml and also from the source code in the python files and invokes a `spec version apply`
   ```yaml
    apiVersion: fission.io/v1
    kind: Function
    metadata:
      name: bar
      namespace: default
    spec:
      environment:
        name: python-27
        namespace: default
      package:
        packageref:
          name: src-pkg
          namespace: default
        functionName: bar.main
    
    ---
    apiVersion: fission.io/v1
    kind: Package
    metadata:
      name: src-pkg
      namespace: default
    spec:
      source:
        url: archive://src-archive
      buildcmd: "./build.sh"
      environment:
        name: python-27
        namespace: default
    status:
      buildstatus: pending
    
    ---
    kind: ArchiveUploadSpec
    name: src-archive
    include:
      - "src/*.py"
      - "src/*.sh"
      - "src/requirements.txt"
    
   ```  
   1. then fission code will first create a new version of package because the contents of archive changed (devoid of source code related to function `"foo"`).
   2. so naturally, a new version of function `"bar"` gets created (because the package contents changed)
   3. Next, all function objects of `"foo"` get deleted, along with its `FunctionVersionTracker` object. 
   4. finally, k8s cluster will have the following objects
   ```json
       {
            "kind": "FunctionVersionTracker",
            "metadata": {
                "name": "bar"
            },
            "spec": {
                "versions": ["uid", "bla", "ueo"]
            } 
       },
       {
            "kind": "PackageVersionTracker",
            "metadata": {
                "name": "src-pkg"
            },
            "spec": {
                "versions": ["uid", "bla", "ueo"]
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "uid"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "foo",
                       "version": "uid"
                     },
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "uid"
                     },
                     {
                       "type": "package",
                       "name": "foo-pkg",
                       "version": "uid" 
                     }
                ]  
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "bla"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "foo",
                       "version": "bla"
                     },
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "bla"
                     },
                     {
                       "type": "package",
                       "name": "src-pkg",
                       "version": "bla" 
                     }
                ]  
            } 
       },
       {
            "kind": "GroupVersionTracker",
            "metadata": {
                "name": "ueo"
            },
            "spec": {
                "git": {
                   "commit": "<git_commit_sha>",
                   "branch": "<git_branch_name>",
                   "clean": "yes"
                },
                "objects": [
                     {
                       "type": "function",
                       "name": "bar",
                       "version": "ueo"
                     },
                     {
                       "type": "package",
                       "name": "src-pkg",
                       "version": "ueo" 
                     }
                ]  
            } 
       }
   ```
At this point, more scenarios can be added for `deletion`, but I'm not sure if this approach for deleting versions w.r.t `spec` is fundamentally wrong. Before I waste more time on thinking along these lines, I would like to hear your opinion.

### Pseudocode for spec
This pesudocode doesnt do justice to the exact code that would be needed, but just outlines the general functionality
```go
func specVersionApply() {
        // 1. generate a unique uid for this iteration of apply
        
        // 2. collect all the objects from the user's yaml into a global fissionResources struct (similar to specApply)
        
        // 3. apply individual objects starting from archives, packages, functions, env and triggers.
        for objType := range [ "archives", "packages", "functions", "environment", "triggers" ] {
                groupVersionTracker := {}
                
                // here, the way we need to check if the item is present is slightly different from spec, only for functions and packages, because their names on the cluster and those in the spec will never match due to version suffixes.
                // so, in the following pseudocode, anytime there is deepCmp, please note that it's not comparing the names.
                if objType == Package {
                           for each element in fissionResources.Packages (collected as part of step 2) 
                               1. get the PackageVersionTracker object for this element
                               2. existingPkgObj = find the latest version from this object and get the package object for this version
                               3. neededPkgObj = elem
                               4. hasChanged = deepCmp(existingPkgObj, neededPkgObj)
                               
                               // For explanation, refer to scenario 3 above (observe the second GroupVersionTracker specifically)
                               5. if !hasChanged, update the last version in the groupVersionTracker
                               
                               6. if hasChanged {
                                        create a new PackageObject with neededPkgObj body, but name suffixed with uid generated in step 1
                                        create/update the `PackageVersionTracker` for this packageName
                                        update the `GroupVersionTracker`
                                  } 
                }
                
                else if objType == Function {
                           for each element in fissionResources.Functions (collected as part of step 2) 
                               1. funcVersionTracker = get the FunctionVersionTracker object for this element
                               2. existingFuncObj = find the latest version for this function from funcVersionTracker and get the function object
                               3. neededFuncObj = elem
                               4. //we need to make a few manual comparisons of the function spec fields to detect if a function needs to be updated, i.e, a new version needs to be created or not. This is more like the 3 way merge of kubectl apply
                                  4i.  funcObject := newFuncObj{}
                                  4ii. pkgNameHasChanged = cmp(neededFuncObj.Spec.PackageRef.Name, existingFuncObj.Spec.PackageRef.Name)  or cmp(neededFuncObj.Spec.PackageRef.Namespace, existingFuncObj.Spec.PackageRef.Namespace)
                                       if pkgNameHasChanged {
                                                newPkgObj = get the latest version of that package object && funcObject.Spec.PkgRef = newPkgObj's name, namespace, resourceVersion 
                                                hasChanged = True
                                       }
                                       if !pkgNameHasChanged{
                                                latestPackageObj = get the latest version of the package object this function is refering to (that may have been created as above)
                                                isDiff = cmp(latestPackageObj.name, existingFuncObj.Spec.PackageRef.name)
                                                if isDiff, then, it means a latest version of this package exists {
                                                        funcObject.Spec.PackageRef = latestPackageObj's name, namespace, resourceVersion.
                                                        hasChanged = True
                                                } 
                                       }
                                  4iii. //cmp the remaining fields of the funcSpec with deepCmp 
                                        isDiff = cmp(existingFuncObj.Spec.remainingFields, neededFuncObj.Spec.remainingFields)
                                        if isDiff {
                                                update the funcObject.Spec.necessaryFields
                                                hasChanged = True
                                        }
                               
                               5. if !hasChanged, update the last version in the groupVersionTracker
                               
                               6. if hasChanged {
                                        create funcObject
                                        create/update the `FunctionVersionTracker` for this elem.Name
                                        update the `GroupVersionTracker`
                                  } 
                }
                
                else {
                        same behavior as `spec apply`
                }
                
                // TODO : Not able to decide on the delete behavior.
                // calculate the objects that need to be deleted
                if item existed before but not present in the list of fissionResources in this iteration, then
                        if item.Type == Function, delete all versions of the function and delete `FunctionVersionTracker` obj for this functionName 
                        if item.Type == Package{
                                delete all versions of the package and delete `PackageVersionTracker` obj for this packageName. 
                                We'll also have to delete all functions refering to this package? What if user's spec still has the function object but not the package? Will that be a user error that needs to be validated even before `spec version apply`
                        }  
                        else, delete the item
        
        }
}
```
## Implementation details for fission cli commands
Dividing this section into 
1. TL;DR walking you through various scenarios and how the code intends to behave and the snapshot of objects created etc. 
2. Pseudocode - for version create, update, delete, list

### TL;DR walking you through various scenarios
The list of scenarios are again not definitely not all-encompassing.
Summary of the scenarios (CRUD) :
scenario 1 touches upon creation of different versions of packages. 
scenarios 2 - 3 touch upon creation of different versions of functions.
scenario 4 touches upon updation of specific versions of packages.
scenarios 5 - 6 touch upon updation of specific versions of functions.  
scenarios 7 - 8 touch upon deletion of specific versions of packages.
scenarios 9 - 10 touch upon deletion of specific versions of functions.
scenarions 11 - 12 Listing all versions of a package and function respectively.

( I havent listed the scenarios for CRUD on package versions first and then function verstions. Instead, I've interleaved the scenarios for package and function version CRUD operations. This might seem a little odd at first but it's necessary for ex: in order to understand package version update, it's important to understand how function versions are created first. )

Scenarios:
#### Version create for packages
The general format of the cli to create versioned packages is `fission package version create --name <pkg> --other-options...`

1. Lets say, a user wants to create a versioned package with a source archive, he can do so with `fission package version create --name src-pkg --src src-archive.zip --env python --buildcmd "./build.sh"`
   
   First, cli code will create a random uid and create a package object with name suffixed with uid. 
   Next, it will create an object of type `PackageVersionTracker` for this package by appending the newly created package version.
   He   re's a snapshot of the objects on the cluster at the end of this scenario :
   ```json
   {
        "kind": "PackageVersionTracker",
        "metadata": {
            "name": "src-pkg"
        },
        "spec": {
            "versions": ["bla"]
        } 
   },
   { 
        "kind": "Package",
        "metadata": {
            "name": "src-pkg-bla",
            "annotations": {
               "versioned" : true 
            }
        },
        "spec": {
            "source": {
                "checksum": {},
                "literal": "<literal>",
                "type": "literal"
            },
            "environment": {
                "name": "python",
                "namespace": "default"
            },
            "deployment": {
                "checksum": {}
            }
        },
        "status": {
            "buildstatus": "pending"
        }
   }
   ```

#### Version create for function
The general format of the cli to create versioned functions is `fission function version create --name <function> --other-options...`

2. Continuing from scenario 1, let's say a user wants to create a function with this package, he can do so with `fission function version create fnA --pkg src-pkg --version bla --other-options`.
   
   At this point, a function object is created with the same version uid supplied and packageRef in the function will refer to this package version. Also the `FunctionVersionTracker` object is created with this newly created version.


3. This scenario talks about how function version creation is handled when a user creates a function without supplying `--pkg` option. For ex, a user can create a function version with `function version create --name foo --code foo.py --env foo-env`.
   
   First, the fission code generates a unique version uid. 
   Next, since fission needs to create a package in such a case, it will implicitly create a versioned package with the version uid just generated, 
   It will then go ahead and create function object with this package reference.
   Also `PackageVersionTracker` and the `FunctionVersionTracker` objects are created accordingly.

Note: If a function version creation is attempted by the user using `--pkg` flag and the supplied package is not a verisoned package, the plan is to error out.   

#### Version update for packages
The general format of the cli to update versions of packages is `fission package version update --name src-pkg --other-options`. Idea here is that every time the user invokes a `package version update`, a new version of package object is created.

4. Lets say the user wants to update the package that was created as part of scenario 1 above with updated source archive `fission package version create --name src-pkg --src src-archive.zip --env python --buildcmd "./build.sh"`

   1. As expected, a new version uid is generated and a package object is created with the given name suffixed by the version uid. Next, the "versions" list in the `PackageVersionTracker` object created above will have this version uid appended to it.
   What happens next depends on if one(many) function(s) reference this package.
   2. If no function references this package, we do nothing.
   3. Let's say there's one versioned function that references the previous version of the package. We have two choices now - 1. go ahead and update the function to refer to this newly created package version or 2. do nothing.
      I like the idea of doing nothing better in this case. This is because, the user is in full control of the cli and he can create a new function version (by updating function object as described in scenario 5 below) using this version of the package. 
      Please note that this behavior is different from that of `spec version apply`. There, if a package reference of a function object is updated, automatically, the function will also be updated resulting a new version. 

#### Version update for functions      
The general format of the cli to update versions of function is `fission function version update --name <function> --other-options...`

5. Let's assume there is a function `"foo-uid"` created with a package `"foo-pkg-uid"`. Next, let's say the user creates a new version of the package by updating the source code (as described in the above scenario) and he wants to update this function `foo` to use this package version, for ex: bla , he can do so using `fission function version update --name foo --pkg foo-pkg --version bla`.
   At this point, the fission function will create a new function version using the name `"foo"` suffixed by the input version uid, in this example "bla". It will also update the `FunctionVersionTracker` object accordingly.
   
6. Let's assume a scenario where a function is updated without the `--pkg` option, instead, with `--code` and the other required flag. Ex : `fission function version update --name bar --code bar.py --other-options`.
   First, the fission code generates a unique version uid. 
   Next, it will create a new version of a package with the version uid just generated, 
   It will then go ahead and create function object with this package reference.
   Also `PackageVersionTracker` and the `FunctionVersionTracker` objects are updated accordingly.
   
Note : Not sure if we should support an `--override` option for package updates and function updates. What do you think?
   
#### Version delete for packages
The general format of the cli to delete versioned packages is `fission package version delete --name <pkg> --version <version-uid>`

7. Let's say there are 2 versions of a package `bar-pkg` and the user wants to delete one of the versions, he can do so with `fission package version delete --name bar-pkg --version <version-uid>`.
   First, fission code will find out if there are any functions refering to this package using `FunctionVersionTracker` object. 
   If there are any, it will delete all of those functions and delete the versions of those functions from the corresponding `FunctionVersionTracker` objects.
   If there are none, it will just delete the package object and remove this version from `PackageVersionTracker` object.
   
8. I also plan to support a string `"all"` for value of version in the above command. This is to help users wipe out all versions of a particular package.
   so, the user can invoke  `fission package version delete --name <pkg> --version all`.
   At this point, the same behavior as the above scenario will be exhibited. Only, this time, a check is made to see if there are functions referencing each version of the package and if there are, they will all be cleaned up.
   
#### Version delete for functions
The general format of the cli to delete versioned functions is `fission function version delete --name <function> --version <version-uid>`

9. Let's say there are 2 versions of a function `"foo"` and the user wants to delete one version with `fission function version delete --name foo --version <version-uid>`.
   First, fission code will delete the function objects and remove version uids from `FunctionVersionTracker` of the corresponding function. 
   Note that, the packages are not deleted, just like `fission function delete` cli's behavior
   
10. Similar to packages, the value of "all" for version in the `fission function version delete --name <function> --version all` will cause fission code to delete all versions of the function and cleanup the `FunctionVersionTracker` object.

#### Version list for packages
The general format of the cli to list all versions of a package is `fission package version list --name <pkg>`

11. When the user issues this command to list all versions of a package, the fission code fetches the versions list from `PackageVersionTracker` object.

#### Version list for functions
12. Similar to package version list, `fission function version list --name <function>` can be used to list all versions of a function. The details are fetched from `FunctionVersionTracker` object.
    
### Pseudocode
Since the explanation in scenarios is pretty much what the pseducode would look like, I ended up avoiding a re-write of it.
However, should there be any questions on the behavior of code for any of the cli options, please let me know.

# Note
1. The use-cases 2 and 3 listed under the very first section will be addressed in a separate review.
2. Through out the document, fission code implies code in CLI and the controller.

# Conclusion 
This is my take on versioning (obviously after a couple of discussions with Soam). YMMV.
If this looks like a recipe for disaster and/or you have suggestions on simplifying it or have a better solution in mind, I'd love to hear them all.