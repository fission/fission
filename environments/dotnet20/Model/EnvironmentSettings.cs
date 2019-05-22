using System;
using System.Collections.Generic;
using System.Text;

namespace Fission.DotNetCore.Model
{
    public class EnvironmentSettings
    {
        public string LogDirectory { get; set; }
        
        public string DllDirectory { get; set; }
        public string functionBodyFileName { get; set; }
        public string functionSpecFileName { get; set; }
        public bool RunningOnwindows { get; set; }
    }
}
