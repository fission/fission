using System;
using System.Reflection;
using Fission.DotNetCore.Api;

namespace Fission.DotNetCore.Compiler
{
    class Function
    {
        private readonly Assembly _assembly;
        private readonly Type _type;
        private readonly MethodInfo _info;

        public Function(Assembly assembly, Type type, MethodInfo info)
        {
            if (info == null) throw new ArgumentNullException(nameof(info));
            if (assembly == null) throw new ArgumentNullException(nameof(assembly));
            if (type == null) throw new ArgumentNullException(nameof(type));
            _assembly = assembly;
            _type = type;
            _info = info;
        }

        public object Invoke(FissionContext context)
        {
            return _info.Invoke(_assembly.CreateInstance(_type.FullName), new[] { context });
        }
    }
}
