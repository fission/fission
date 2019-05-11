using Builder.Model;
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.RegularExpressions;

namespace Builder.Utility
{

    public sealed class BuilderHelper
    {

        private static readonly Lazy<BuilderHelper> lazy =
            new Lazy<BuilderHelper>(() => new BuilderHelper());

        public string _logFileName = string.Empty;
        public static BuilderHelper Instance { get { return lazy.Value; } }

        private static Logger _logger = new Logger("Initial.log");

        private BuilderSettings _builderSettings = null;

        public BuilderSettings builderSettings
        {
            get
            {
                if (_builderSettings == null)
                {
                    string builderSettingsjson = GetBuilderSettingsJson();
                    _builderSettings = ObjectConverter.Instance.GetBuilderSettingsFromJson(builderSettingsjson);
                }
                return _builderSettings;
            }
            set
            {
                builderSettings = value;
            }
        }

        public Logger logger
        {
            get
            {
                return _logger;
            }
            set
            {
                _logger = value;
            }
        }

        static BuilderHelper()
        {
        }
        private BuilderHelper()
        {
        }

        private string GetBuilderSettingsJson()
        {
            var baselocation = AppDomain.CurrentDomain.BaseDirectory;
            var FileLocation = baselocation + "builderSettings.json";

            return File.ReadAllText(FileLocation);
        }


        public List<IncludeNuget> GetNugettoInclude(string directoryPath)
        {
            List<IncludeNuget> includeNugets = new List<IncludeNuget>();
            string includeNugetsFilePath = Path.Combine(directoryPath, this.builderSettings.NugetSpecsFile);
            if (File.Exists(includeNugetsFilePath))
            {
                Regex _pkgName = new Regex(this.builderSettings.NugetPackageRegEx,
                                            RegexOptions.Compiled);

                string filetext = File.ReadAllText(includeNugetsFilePath);
                var _pkgMatchCollection = _pkgName.Matches(filetext);

                foreach (Match match in _pkgMatchCollection)
                {
                    if(!string.IsNullOrWhiteSpace(match.Value))
                    {
                        string package = match.Groups["package"]?.Value?.Trim();
                        string version = match.Groups["version"]?.Value?.Trim();
                        this.logger.Log($"adding  {package} | {version} to includeNugets collection");

                        includeNugets.Add(
                                           new IncludeNuget()
                                                {
                                                    packageName = package,
                                                    version = version
                                                }
                                         );
                    }
                   
                }
            }

            return includeNugets;
        }
        public List<ExcludeDll> GetDllstoExclude(string directoryPath)
        {
            List<ExcludeDll> excludeDlls = new List<ExcludeDll>();
            string excludeDllsFilePath = Path.Combine(directoryPath, this.builderSettings.DllExcludeFile);
            if (File.Exists(excludeDllsFilePath))
            {
                //xyzPackage:abc.dll
                Regex _exclude = new Regex(this.builderSettings.ExcludeDllRegEx,
                                            RegexOptions.Compiled);

                string filetext = File.ReadAllText(excludeDllsFilePath);
                var _excludeMatchCollection = _exclude.Matches(filetext);

                foreach (Match match in _excludeMatchCollection)
                {
                    if (!string.IsNullOrWhiteSpace(match.Value))
                    {
                        string _package = match.Groups["package"]?.Value?.Trim();
                        string _dllName = match.Groups["dll"]?.Value?.Trim();
                        this.logger.Log($"adding  {_package} | {_dllName} to excludeDlls collection");

                        excludeDlls.Add(
                                new ExcludeDll()
                                    {
                                        packageName = _package,
                                        dllName = _dllName
                                    }
                                );
                    }
                }
            }

            return excludeDlls;
        }

        public string DeepException(Exception ex)
        {
                string responce = string.Empty;

                responce = " Exception : LEVEL 1: " + Environment.NewLine + ex.Message;
                if (ex.InnerException != null)
                {
                    responce = responce + Environment.NewLine + "LEVEL 2:" + Environment.NewLine + ex.InnerException.Message;
                    if (ex.InnerException.InnerException != null)
                    {
                        responce =responce + Environment.NewLine + "LEVEL 3:" + Environment.NewLine + ex.InnerException.InnerException.Message;

                        if (ex.InnerException.InnerException.InnerException != null)
                        {
                            responce = responce + Environment.NewLine + "LEVEL 4:" + Environment.NewLine + ex.InnerException.InnerException.InnerException.Message;
                            if (ex.InnerException.InnerException.InnerException.InnerException != null)
                            {
                                responce = responce + Environment.NewLine + "LEVEL 5:" + Environment.NewLine + ex.InnerException.InnerException.InnerException.InnerException.Message;
                            }
                        }
                    }
                }

                if(ex.StackTrace!=null)
                {
                    responce = responce + "|| STACK :"+ ex.StackTrace;
                }
                return responce;

            
        }

        public  void WriteTofile(string filenameWithPath , string content)
        {
            using (StreamWriter sw = new StreamWriter(filenameWithPath, false))
            {
                sw.AutoFlush = true;
                sw.Write(content);
            }

        }
    }
}
