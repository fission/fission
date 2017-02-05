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
