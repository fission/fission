using Fission.DotNetCore.Api;
using Fission.DotNetCore.Model;

using Newtonsoft.Json;
using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;

namespace Fission.DotNetCore.Utilty
{
    public sealed class EnvironmentHelper
    {
        private static readonly Lazy<EnvironmentHelper> lazy =
            new Lazy<EnvironmentHelper>(() => new EnvironmentHelper());

        public static EnvironmentHelper Instance { get { return lazy.Value; } }

       public EnvironmentSettings environmentSettings
        {
            get
            {
                if (_envSettings == null)
                {
                    string watcherSettingsjson = GetEnvironmentSettingsJson();
                    _envSettings = ObjectConverter.Instance.GetWatcherSettingsFromJson(watcherSettingsjson);
                }
                return _envSettings;
            }
            set
            {
                _envSettings = value;
            }
        }

        private EnvironmentSettings _envSettings;
        

        static EnvironmentHelper()
        {
        }
        private EnvironmentHelper()
        {
        }

        public  void writeToFile(string msg)
        {

            using (StreamWriter sw = new StreamWriter("main.log",true))
            {
                sw.AutoFlush = true;
                sw.WriteLine($"{DateTime.Now} : {msg}");

            }

        }
        
        private string GetEnvironmentSettingsJson()
        {
            var baselocation = AppDomain.CurrentDomain.BaseDirectory;
            var FileLocation = baselocation + "envsettings.json";

            return File.ReadAllText(FileLocation);
        }

        public BuilderRequest GetBuilderRequest(string json)
        {
            BuilderRequest builderRequest = new BuilderRequest();
            try
            {
                builderRequest = JsonConvert.DeserializeObject<BuilderRequest>(json);
            }
            catch(Exception ex)
            {
                Console.WriteLine("Error : Unable to intersept request json " + ex.Message + ex.StackTrace);
            }

            return builderRequest;
        }

        public List<DllInfo> GetDllInfoFromDirectory(string directorypath)
        {
            List<DllInfo> dllInfos = new List<DllInfo>();

            Console.WriteLine($"finding dll in folder {directorypath}");
            DirectoryInfo d = new DirectoryInfo(directorypath);//Assuming packagepath is your Folder
           var files = d.GetFiles("*.dll"); //Getting dll files
            foreach(var file in files)
            {
                Console.WriteLine($"found dll  {file.Name.ToLower()} at {Path.Combine(directorypath, file.Name)}");
                dllInfos.Add(new DllInfo() {
                    name = file.Name.ToLower(),
                    path = Path.Combine(directorypath, file.Name)
                });
            }

            return dllInfos;
        }

        public FunctionSpecification GetFunctionSpecs(string directorypath)
        {

            string functionSpecsFilePath = Path.Combine(directorypath, this.environmentSettings.functionSpecFileName);
            if (File.Exists(functionSpecsFilePath))
            {
                string specsJson = File.ReadAllText(functionSpecsFilePath);
                return ObjectConverter.Instance.GetFunctionSpecificationFromJson(specsJson);
            }
            else
                throw new Exception($"Function Specification file not found at {functionSpecsFilePath}");


        }
    }
}
