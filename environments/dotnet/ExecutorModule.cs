using Fission.DotNetCore.Compiler;
using Fission.DotNetCore.Api;
using System.Collections.Generic;
using Nancy;
using System.IO;
using System;

namespace Fission.DotNetCore
{
    public class ExecutorModule : NancyModule
    {
#if DEBUG
        private const string CODE_PATH = "/tmp/func.cs";
#else
        private const string CODE_PATH = "/userfunc/user";
#endif
        private static Function _userFunc;
        private static Logger _logger = new Logger();

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
                    _logger.WriteError(errstr);
                    var response = (Response)errstr;
                    response.StatusCode = HttpStatusCode.InternalServerError;
                    return response;

                }
                return null;
            }
            else
            {
                var errstr = $"Unable to locate code at '{CODE_PATH}'";
                _logger.WriteError(errstr);
                var response = (Response)errstr;
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
            try
            {
                return _userFunc.Invoke(new FissionContext(args, new Logger()));
            }
            catch (Exception e)
            {
                _logger.WriteError(e.ToString());
                var response = (Response)e.Message;
                response.StatusCode = HttpStatusCode.BadRequest;
                return response;
            }
        }
    }
}

