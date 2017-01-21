using Fission.DotNetCore.Compiler;
using System.Collections.Generic;
using Nancy;
using System.IO;
using System;

namespace Fission.DotNetCore
{
    public class ExecutorModule : NancyModule
    {
        private const string CODE_PATH = "/userfunc/user";

        private static Function _userFunc;

        public ExecutorModule()
        {
            Post("/specialize", args => Specialize());
            Get("/", _ => Run());
            Post("/", _ => Run());
            Put("/", _ => Run());
            Head("/", _ => Run());
            Options("/", _ => Run());
            Delete("/", _ => Run());

        }

        private object Specialize()
        {
            var errors = new List<string>();
            if (File.Exists(CODE_PATH))
            {
                var code = File.ReadAllText(CODE_PATH);
                _userFunc = FissionCompiler.Compile(code, out errors);
                if (_userFunc == null)
                {
                    var errstr = string.Join(Environment.NewLine, errors);
                    Console.WriteLine(errstr);
                    var response = (Response)errstr;
                    response.StatusCode = HttpStatusCode.InternalServerError;
                    return response;

                }
                return null;
            }
            else
            {
                var response = (Response)"Unable to locate code";
                response.StatusCode = HttpStatusCode.InternalServerError;
                return response;
            }
        }

        private object Run()
        {
            if (_userFunc == null)
            {
                var response = (Response)"Generic container: no requests supported";
                response.StatusCode = HttpStatusCode.InternalServerError;
                return response;
            }
            var args = ((DynamicDictionary)Request.Query).ToDictionary();
            return _userFunc.Invoke(args);
        }
    }
}
