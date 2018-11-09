# Integrating with external systems in Fission

Most common systems that a function interacts with are datastores (databases, object stores), event queues, streams of various kinds etc. Fission has a good way to integrate with message queue integration. These integrations provide a way to integrate with the messaging queue without having to write the connection logic for those systems. In a very similar way a fission function should be able to interact with other systems without the need to write boilerplate code. This document will explore the aspects of building such a feature which will allow user to.

Let's talk in context of three distinct kind of systems and their typical usage:

- Database (For ex. MySQL, AWS RDS)
  - CRUD operations
- Object storage (S3, Minio)
  - List files, Read specific files
  - Be notified of new files in a bucket or a file getting updated
- Streaming (AWS Kinesis)
  - Listen on a new event

## Use cases

### 1: Listing files from S3 bucket

As of today if a function user has to talk to S3 bucket, they will create a config object and a S3 object. These objects will be then used to talk to S3 and do actual operations.


// Think of imports - for classes used
// Additional plugin for extensions instead of in env plugin?

```go 
// Load credentials from the shared credentials file ~/.aws/credentials
sess, err := session.NewSession(&aws.Config{
    Region: aws.String("us-west-2")},
)
// Create S3 service client
svc := s3.New(sess)

result, err := svc.ListBuckets(nil)
if err != nil {
    exitErrorf("Unable to list buckets, %v", err)
}

fmt.Println("Buckets:")

```

- Would it be nice if the s3 object is available to function as part of the context object and the user does not have to write the boilerplate code of creating connection to S3? It seems trivial at least with code for S3.

- More than one function might re-write same logic, so to an extent it makes sense to abstract out the connection logic.

- Implementation will be environment specific as we need to pass a runtime object if we need to do this.

### 2: Reading an event from stream

Taking the previous example a step ahead - what if function could directly get a specific event from a streaming platform instead of the getting the stream object from which event could be retrieved.

```go
func Handler(c runtime.Context, w http.ResponseWriter, r *http.Request) {
    for _, record := range context.events.KinesisEvent.Records {
        kinesisRecord := record.Kinesis
        dataBytes := kinesisRecord.Data
        dataText := string(dataBytes)

        fmt.Printf("%s Data = %s \n", record.EventName, dataText) 
    }

}
```

### 3: Writing result as a row to DB

Similar to getting a record directly within a function, a function might want to write a DB record without the need to handle the connection logic. For example a function will simply return the JSON representation which Fission can convert and store in the table specified and DB which is connected to function through an external entity.

This is probably not possible in near term as it would require post execution hooks etc. around function execution.

## Other complexities

### Latency effects

Any additional work such as making connections might extend the specialization time or the time to execute a function.

### Connection Pools

- With things like relational DB, creating a connection is costly and hence connection pools are created.

- Need to explore lightweight connection poolers like https://github.com/pgbouncer/pgbouncer 

### Time & durability guaranty

- For things such as streams, we need to provide some kind of guaranty eventually in terms of delivery of a event and associated latency.

## Related exploration (TBD)

### Service Catalog

Using https://github.com/kubernetes-incubator/service-catalog to provision and connect to external services. Specifically the BindService is of interest - but it is not clear if you can use an existing service (i.e. without provisioning) to bind to from a Kubernetes pod.

### Cloud Events

Explore if it is possible to use cloud events instead of custom building a event format: https://github.com/cloudevents/sdk-go

## Putting it together

It makes sense to do both (1) and (2) above though the second will require a additional controller loop.

Let's start with the first scenario and a simple implementation - binding which abstracts a resource and it's connection details.

```go
BindingSpec struct {
		FunctionReference []FunctionReference 
        Type              BindingType
        Connection        BindingConnection // How does this work for different types? Multi config and secrets
    }
```

If a function has a binding, the context object will be added with the object related to binding. This implementation will be environment specific.

```go

return func(w http.ResponseWriter, r *http.Request) {
            c := context.New()

            sess, err := session.NewSession(&aws.Config{
                Region: aws.String("us-west-2")},
            )
            c["name-of-binding"] = s3.New(sess) // Which of these are actual network calls?

            h(c, w, r)
        }


```

AI

1) Take a use case and then compare 
2) Specs - on env.
3) Extensions - Library?
4) 