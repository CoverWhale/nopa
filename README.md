# nopa

NATS + OPA 

Nopa is a simple way to store OPA bundles in NATS object storage and have the bundle updated in real time. 

## Usage
Using the [example](example/main.go) application:

- Create a NATS object bucket: `nats obj add bundles`
- Add the bundle to the object store: `nats obj put bundles bundle.tar.gz`
- Start the server `go run main.go`
- Send a request to the service `nats req test '{"package": "data.foo", "input": {"foo": "bar"}}'`

> [!TIP]
> It is up to your application to handle how the package name and input are generated. You could for example use the subject as the package name.
