using System.IO;
using System.Runtime.Serialization.Json;
using Fission.DotNetCore.Api;

public class FissionFunction
{
    public string Execute(FissionContext context)
    {
        var person = Person.Deserialize(context.Request.Body);
        return $"Hello, my name is {person.Name} and I am {person.Age} years old.";
    }
}

public class Person
{
    public string Name { get; set; }
    public int Age { get; set; }

    public static Person Deserialize(Stream json)
    {
        var serializer = new DataContractJsonSerializer(typeof(Person));
        return (Person)serializer.ReadObject(json);
    }
}
