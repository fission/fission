using Fission.DotNetCore.Model;
using System;
using System.Collections.Generic;
using System.Text;

namespace Fission.DotNetCore.Model
{
    public class FunctionSpecification
    {
        public FunctionSpecification()
        {
            this.libraries = new List<Library>();
        }
        public string functionName { get; set; }
        public List<Library> libraries { get; set; }
        public string hash { get; set; }
        public string certificatePath { get; set; }
    }
    public class Library
    {
        public Library()
        {

        }

        public Library(DllInfo dllInfo)
        {
            this.name = dllInfo.name;
            this.nugetPackage = dllInfo.rootPackage;
            this.path = dllInfo.path;
        }
        public string name { get; set; }
        //public string version { get; set; }
        public string path { get; set; }
        public string nugetPackage { get; set; }
    }
}
