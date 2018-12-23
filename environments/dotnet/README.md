# Fission: dotnet C# Environment

This is a simple dotnet C# environment for Fission.

It's a Docker image containing the dotnet 1.1.0 runtime. The image 
uses Kestrel with Nancy to host the internal web server and uses 
Roslyn to compile the uploaded code.

The image supports compiling and running code with types defined in
mscorlib and does not at present support other library references.
One workaround for this would be to add the references to this project's
project.json file and rebuild the container.

The environment works via convention where you create a C# class
called FissionFunction which has a  method named Execute taking a single
parameter, a FissionContext object.

The FissionContext object gives access to the arguments and other items 
like logging. Please see FissionContext.cs for public API.

Example of simplest possible class to be executed:

```
using System;
using Fission.DotNetCore.Api;

public class FissionFunction {
    public string Execute(FissionContext context) {
        return null;
    }
}
```

Please see examples below.

## Rebuilding and pushing the image

To rebuild the image you will have to install Docker with version higher than 17.05+
in order to support multi-stage builds feature.  

### Rebuild containers

Move to the directory containing the source and start the container build process:

```
docker build -t USER/dotnet-env .
```

After the build finishes push the new image to a Docker registry using the 
standard procedure.

## Echo example

### Setup fission environment
First you need to setup the fission according to your cluster setup as 
specified here: https://github.com/fission/fission


### Create the class to run

Secondly you need to create a file /tmp/func.cs containing the following code:

```
using System;
using Fission.DotNetCore.Api;

public class FissionFunction 
{
    public string Execute(FissionContext context){
        context.Logger.WriteInfo("executing.. {0}", context.Arguments["text"]);
        return (string)context.Arguments["text"];
    }
}
``` 
### Run the example

Lastly to run the example:

```
$ fission env create --name dotnet --image fission/dotnet-env

$ fission function create --name echo --env dotnet --code /tmp/func.cs

$ fission route create --method GET --url /echo --function echo

$ curl http://$FISSION_ROUTER/echo?text=hello%20world!
  hello world
```

## Addition service example

### Setup fission environment
First you need to setup the fission according to your cluster setup as 
specified here: https://github.com/fission/fission


### Create the class to run

Secondly you need to create a file /tmp/func.cs containing the following code:

```
using System;
using Fission.DotNetCore.Api;

public class FissionFunction 
{
    public string Execute(FissionContext context){
        var x = Convert.ToInt32(context.Arguments["x"]);
        var y = Convert.ToInt32(context.Arguments["y"]);
        return (x+y).ToString();
    }
}
``` 
### Run the example

Lastly to run the example:

```
$ fission env create --name dotnet --image fission/dotnet-env

$ fission function create --name addition --env dotnet --code /tmp/func.cs

$ fission route create --method GET --url /add --function addition

$ curl "http://$FISSION_ROUTER/add?x=30&y=12"
  42
```

## Accessing http request information example

### Setup fission environment
First you need to setup the fission according to your cluster setup as 
specified here: https://github.com/fission/fission


### Create the class to run

Secondly you need to create a file /tmp/func.cs containing the following code:

```
using System;
using Fission.DotNetCore.Api;

public class FissionFunction
{
    public string Execute(FissionContext context){
        var buffer = new System.Text.StringBuilder();
        foreach(var header in context.Request.Headers){
                buffer.AppendLine(header.Key);
                foreach(var item in header.Value){
                        buffer.AppendLine($"\t{item}");
                }
        }
        buffer.AppendLine($"Url: {context.Request.Url}, method: {context.Request.Method}");
        return buffer.ToString();
    }
}

``` 
### Run the example

Lastly to run the example:

```
$ fission env create --name dotnet --image fission/dotnet-env

$ fission function create --name httpinfo --env dotnet --code /tmp/func.cs

$ fission route create --method GET --url /http_info --function httpinfo

$ curl "http://$FISSION_ROUTER/http_info"
Accept
	*/*;q=1
Host
	fissionserver:8888
User-Agent
	curl/7.47.0
Url: http://fissionserver:8888, method: GET

```

## Accessing http request body example

### Setup fission environment
First you need to setup the fission according to your cluster setup as 
specified here: https://github.com/fission/fission


### Create the class to run

Secondly you need to create a file /tmp/func.cs containing the following code:

```
using System.IO;
using System.Runtime.Serialization.Json;
using Fission.DotNetCore.Api;

public class FissionFunction
{
    public string Execute(FissionContext context)
    {
        var person = Person.Deserialize(context.Request.Body);
        return $"Hello, my name is {person.Name} and I am {person.Age} years old.";
    }
}

public class Person
{
    public string Name { get; set; }
    public int Age { get; set; }

    public static Person Deserialize(Stream json)
    {
        var serializer = new DataContractJsonSerializer(typeof(Person));
        return (Person)serializer.ReadObject(json);
    }
}

``` 
### Run the example

Lastly to run the example:

```
$ fission env create --name dotnet --image fission/dotnet-env

$ fission function create --name httpbody --env dotnet --code /tmp/func.cs

$ fission route create --method GET --url /http_body --function httpbody

$ curl -XPOST "http://$FISSION_ROUTER/http_body" -d '{ "Name":"Arthur", "Age":42}'
Hello, my name is Arthur and I am 42 years old.

```


## Developing/debugging the enviroment locally

The easiest way to debug the environment is to open the directory in
Visual Studio Code (VSCode) as that will setup debugger for you the
first time.

Remember to install the excellent extension 
"C# for Visual Studio Code(powered by OmniSharp)" to get statement completion

The class ExecutorModule contain preprocessor directive overriding where 
the input code file should be found:

```
#if DEBUG
        private const string CODE_PATH = "/tmp/func.cs";
#else
        private const string CODE_PATH = "/userfunc/user";
#endif
```

So what you need to do is:
1. Open the directory in VSCode. 
This will prompt restore of packages and query is debugger setup is needed. Accept both prompts.
2. Press F5 to start the web server. Set breakpoints etc..
3. Add a code file containing valid C# at /tmp/func.cs 
4. Specialize the service with curl via post
```
$ curl -XPOST http://localhost:8888/specialize
```
5. Call your function with curl
```
$ curl -XGET http://localhost:8888
``` 
