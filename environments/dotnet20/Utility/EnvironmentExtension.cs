using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;

namespace Fission.DotNetCore.Utilty
{
    public static class EnvironmentExtension
    {
        
        public static IEnumerable<TSource> DistinctBy<TSource, TKey>(
                this IEnumerable<TSource> source,
                Func<TSource, TKey> keySelector)
        {
            var knownKeys = new HashSet<TKey>();
            return source.Where(element => knownKeys.Add(keySelector(element)));
        }
        public static string GetrelevantPathAsPerOS(this string curruntPath)
        {
            if (EnvironmentHelper.Instance.environmentSettings.RunningOnwindows && curruntPath.Contains("\\"))
            {
                return curruntPath;
            }
            else
            {
                return curruntPath.Replace("\\", "/");
            }
        }
    }

   
}
