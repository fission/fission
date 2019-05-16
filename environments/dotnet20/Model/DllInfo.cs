using System;
using System.Collections.Generic;
using System.Text;

namespace Fission.DotNetCore.Model
{
    public class DllInfo
    {
        public string name { get; set; }
        public string rootPackage { get; set; }
        public string framework { get; set; }

        public string processor { get; set; }
        public string path { get; set; }
    }
}
