using System.IO;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Nancy.Owin;

namespace Fission.DotNetCore
{
    public class Program
    {
        public static void Main(string[] args)
        {
            var host = new WebHostBuilder()
               .UseContentRoot(Directory.GetCurrentDirectory())
               .UseKestrel()
               .UseUrls("http://*:8888")
               .Configure(app => app.UseOwin(x => x.UseNancy()))
               .Build();
            host.Run();
        }
    }
}
