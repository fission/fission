//Nugetdownloader.dll from https://github.com/paraspatidar/NugetDownloader
// nuget package : https://www.nuget.org/packages/NugetDownloader/


 Source Package zip :
 --soruce.zip
	|--Func.cs
	|--nuget.txt
	|--exclude.txt
	|--....MiscFiles(optional)
	|--....MiscFiles(optional)
 
 Func.cs --> This contains orignal function body with Executing method name as : Execute
 nuget.txt--> this file contains list of nuget packages required by your function , in this file
			put one line per nuget with nugetpackage name:version(optional) formate 
			Forexample :
				RestSharp
				Newtonsoft.json:10.2.1.0

			this should match the following regex as mentions in builderSetting.json

		"NugetPackageRegEx": "\\:?\\s*(?<package>[^:\\n]*)(?:\\:)?(?<version>.*)?\\n"

		(Note: Please do not forget to add newline /enter in the last line of file else last line will be immited)
  
 exclude.txt--> this file contains list of dlls of specific nuget packages which doesnt need to be
				added during compilation.put one line per nuget with dllname:nugetpackagename
				formate ,For example :
				Newtonsoft.json.dll:Newtonsoft.json

			this should match the following regex as mentions in builderSetting.json

			(Note: Please do not forget to add newline /enter in the last line of file else last line will be immited)

		"ExcludeDllRegEx": "\\:?\\s*(?<package>[^:\\n]*)(?:\\:)?(?<dll>.*)?\\n",

From above , builder will creat a deployment package with all dlls in a folder and one functionspecification file :
 Deployement Package zip :
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
