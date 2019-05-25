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
