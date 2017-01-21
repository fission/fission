using System;
using System.Reflection;
using System.Collections.Generic;

namespace Fission.DotNetCore.Compiler
{
    class Function
    {
        private readonly MethodInfo _info;
        public Function(MethodInfo info)
        {
            if (info == null) throw new ArgumentNullException(nameof(info));
            _info = info;
        }

        public object Invoke(Dictionary<string, object> args)
        {
            return _info.Invoke(null, new[] { args });
        }
    }
}