using System;
using Fission.DotNetCore.Api;

public class FissionFunction 
{
    public string Execute(FissionContext context){
        context.Logger.WriteInfo("executing.. {0}", context.Arguments["text"]);
        return (string)context.Arguments["text"];
    }
}