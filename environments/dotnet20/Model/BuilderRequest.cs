using System;
using System.Collections.Generic;
using System.Text;

namespace Fission.DotNetCore.Model
{
    public class BuilderRequest
    {
        /// <summary>
        /// this is folder path of deployment package which contains all deployment content copied to env
        /// </summary>
        public string filepath { get; set; }
        public string functionName { get; set; }
        public string url { get; set; }
        public FunctionMetadata FunctionMetadata { get; set; }
    }
    public class FunctionMetadata
    {
        public string name { get; set; }
        public string @namespace { get; set; }
        public string selfLink { get; set; }
        public string uid { get; set; }
        public string resourceVersion { get; set; }
        public int generation { get; set; }
        public DateTime creationTimestamp { get; set; }
    }
}
