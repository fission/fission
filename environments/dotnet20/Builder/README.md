# Fission: dotnet 2.0 C# Environment Builder

This is a simple dotnet core 2.0 C# environment builder for Fission.

It's a Docker image containing the dotnet 2.0.0 runtime builder. This image read the source package and uses 
Roslyn to compile the source package code and creates deployment package out of it.
This enables using  nuget packages as part of function and thus user can use extended functionality in fission functions via nuget.


Now , When function is created with the output package , then on function execution when pod is specialized , 
this builder will send the deployment package to function environment /v2/specialized endpoint.

There further envrionment will compile it and execute the function.


Example of simplest possible class to be executed:

The source package structure in zip file :

```
 Source Package zip :
 --soruce.zip
	|--Func.cs
	|--nuget.txt
	|--exclude.txt
	|--....MiscFiles(optional)
	|--....MiscFiles(optional)
```

Func.cs --> This contains orignal function body with Executing method name as : Execute
nuget.txt--> this file contains list of nuget packages required by your function , in this file
		         	put one line per nuget with nugetpackage name:version(optional) formate 
			       Forexample :
```
				   RestSharp
				   Newtonsoft.json:10.2.1.0
```
		  	      this should match the following regex as mentions in builderSetting.json
```
     		      "NugetPackageRegEx": "\\:?\\s*(?<package>[^:\\n]*)(?:\\:)?(?<version>.*)?\\n"
```
	     	      (Note: Please do not forget to add newline /enter in the last line of file else last line will be immited)
  
 exclude.txt--> as nuget.txt will download orignal package and their dependent packages , thus sometime dependent packages might not be
 							that usefull and can break compilation , thus this file contains list of dlls of specific nuget packages which doesnt need to be
				      added during compilation if  they  break compilation .put one line per nuget with dllname:nugetpackagename
				      formate ,For example :
```
			     Newtonsoft.json.dll:Newtonsoft.json
```
			     this should match the following regex as mentions in builderSetting.json

		     	(Note: Please do not forget to add newline /enter in the last line of file else last line will be immited)
```
          		"ExcludeDllRegEx": "\\:?\\s*(?<package>[^:\\n]*)(?:\\:)?(?<dll>.*)?\\n",
```	

From above , builder will creat a deployment package with all dlls in a folder and one functionspecification file :
 Deployement Package zip :
 ```
 --Deploye.zip
	|--Func.cs
	|--nuget.txt
	|--exclude.txt
	|--dll()
		|--newtonsoft.json.dll
		|--restsharp.dll
	|--logs()
		|-->logFileName
	|--funcion.json // this is the functionspecific file
	|--....MiscFiles(optional)
	|--....MiscFiles(optional)
```
Here are commands and detailed example for the same , lets say my source package zip name is *funccsv.zip* :


```
using System;
using Fission.DotNetCore.Api;

public class FissionFunction 
{
    public string Execute(FissionContext context){
	        try
            {
				context.Logger.WriteInfo("Staring..... ");
				respo=$" sample object by getting Enum of   CsvHelper nuget dll: { CsvHelper.Caches.NamedIndex.ToString()}";
            }  
            catch(Exception ex)
            {
				context.Logger.WriteError(ex.Message);
                respo = ex.Message;
            }
		context.Logger.WriteInfo("Done!");
		return respo;
    }
}
```

Here is my nuget.txt

```
CsvHelper
```

As we dont want to exclude any specific dll thus we shall leave exclude.txt as empty.

# Now check name of existing environments & functions as we want to create a unique environment for this dotnetcore if not already present
```
fission env list
fission fn list
 ```
#Create Environment with builder (choose a unique which doesn't exist , here we have chosen : dotnetcorewithnuget  ) 
# also suppose the builder image name is fissionbuilder and hosted on dockerhub as fission/dotnetbuilder
 ```
fission environment create --name dotnetcorewithnuget --image fission/dotnet20-env --builder  fission/dotnetbuilder
 ```
# Verify fission-builder and fission-function namespace for new pods (pods name beginning with env name which we have given like dotnetcorewithnuget-xxx-xxx)
 ```
kubectl get pods -n fission-builder
kubectl get pods -n fission-function
 ```
#Create Package from source zip using this environment name , this will output some package name created..
 ```
fission package create --src funccsv.zip --env dotnetcorewithnuget
 ```
# Note down output package name lets say it is funccsv-zip-xyz now check its status using package info command , this will give the status
 on what happened with builder and test compilation in builder.
 
 ```
fission package info --name funccsv-zip-xyz
```

#Status of package should be failed / running / succeeded .
# Wait if the status is running , until it fails or succeeded.
# For detailed build logs, you can shell into builder pod in fission-builder namespace and verify log location mentioned in above result.


#Now If the result is succeeded , then go ahead and create function using this package
#--entrypoint flag is optional if your function body file name is func.cs  (which it should be as builder need that), else put the filename (without extension )
 ```
 fission fn create --name dotnetcsvtest --pkg funccsv-zip-xyz --env dotnetcorewithnuget --entrypoint "func"
 ```
#Test the function execution :

``` 
 fission fn test --name dotnetcsvtest`
```
above would execute the function and will output the enum value as written in dll.

rest of the feature are same as normal fission environment.

Benifit of using builder :

1. Ability to use various nuget packages.
2. Ability ti use many aditional files and functions as part of deployment package.
3. Ability to know the compilation issue in advance via package logs , instead of environment giving compilation issue.



