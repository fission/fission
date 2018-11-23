using System;
using System.Collections.Generic;
using System.Text;

namespace Builder.Utility
{
    public static class BuilderExtensions
    {
        public static string GetrelevantPathAsPerOS(this string curruntPath)
        {
            if (BuilderHelper.Instance.builderSettings.RunningOnwindows)
            {
                return curruntPath;
            }
            else
            {
                return curruntPath.Replace("\\","/");
            }
        }
    }
}
