using System;
using System.Collections.Generic;
using System.Text;

namespace Builder.Model
{
    public class BuilderSettings
    {
        public string NugetSpecsFile { get; set; }
        public string DllExcludeFile { get; set; }
        public string BuildLogDirectory { get; set; }
        public string NugetPackageRegEx { get; set; }
        public string ExcludeDllRegEx { get; set; }
        public bool RunningOnwindows { get; set; }
        public string functionBodyFileName { get; set; }
        public string functionSpecFileName { get; set; }
        public string DllDirectory { get; set; }
    }
}
