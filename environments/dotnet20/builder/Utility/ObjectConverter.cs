using Builder.Model;
using Newtonsoft.Json;
using System;
using System.Collections.Generic;
using System.Text;

namespace Builder.Utility
{
    public sealed class ObjectConverter
    {

        private static readonly Lazy<ObjectConverter> lazy =
         new Lazy<ObjectConverter>(() => new ObjectConverter());

        public static ObjectConverter Instance { get { return lazy.Value; } }

        private ObjectConverter()
        {

        }


        public BuilderSettings GetBuilderSettingsFromJson(string json)
        {
            return JsonConvert.DeserializeObject<BuilderSettings>(json);
        }



    }
}
