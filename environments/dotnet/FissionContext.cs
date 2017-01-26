using System;
using System.Collections.Generic;

namespace Fission.DotNetCore.Api
{
    public class FissionContext
    {
        public FissionContext(Dictionary<string, object> args, Logger logger)
        {
            if (args == null) throw new ArgumentNullException(nameof(args));
            if (logger == null) throw new ArgumentNullException(nameof(logger));
            Arguments = args;
            Logger = logger;
        }

        public Dictionary<string, object> Arguments { get; private set; }
        public Logger Logger { get; private set; }
    }

    public class Logger
    {
        public void Write(Severity severity, string format, params object[] args)
        {
            Console.WriteLine($"{DateTime.Now.ToString("MM/dd/yy H:mm:ss zzz")} {severity}: " + format, args);    
        }

        public void WriteInfo(string format, params object[] args){
            Write(Severity.Info, format, args);
        }

        public void WriteWarning(string format, params object[] args){
            Write(Severity.Warning, format, args);
        }

        public void WriteError(string format, params object[] args){
            Write(Severity.Error, format, args);
        }

        public void WriteCritical(string format, params object[] args){
            Write(Severity.Critical, format, args);
        }

        public void WriteVerbose(string format, params object[] args){
            Write(Severity.Verbose, format, args);
        }
    }

    public enum Severity
    {
        Info,
        Warning,
        Error,
        Critical,
        Verbose
    }
}