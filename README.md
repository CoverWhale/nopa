# nopa

**NATS + OPA**

Nopa is a simple way to store Open Policy Agent (OPA) bundles in NATS Object Storage and have the bundles updated in real time. It integrates NATS and OPA to provide dynamic policy evaluation within your applications.

## Usage

Using the [example](example/main.go) application:

1. **Create a NATS Object Bucket:**

   ```bash
   nats obj add bundles
   ```

2. **Add the Bundle to the Object Store:**

   ```bash
   nats obj put bundles bundle.tar.gz
   ```

3. **Start the Server:**

   ```bash
   go run main.go
   ```

4. **Send a Request to the Service:**

   ```bash
   nats req test '{"package": "data.foo", "input": {"foo": "bar"}}'
   ```

> **Note:** It is up to your application to handle how the package name and input are generated. You could, for example, use the subject as the package name.

## Overview

Nopa provides an `Agent` that interacts with NATS Object Storage to retrieve and update OPA policy bundles. The agent can:

- **Load Policy Bundles:** Retrieve policy bundles from a NATS Object Store and load them into the agent.
- **Watch for Updates:** Automatically reload bundles when they are updated in the Object Store.
- **Evaluate Policies:** Use OPA to evaluate policies with provided input data.

## Implementation Details

### Agent Structure

The `Agent` struct holds:

- **Store:** The NATS Object Store used to retrieve bundles.
- **Logger:** A logger for logging messages.
- **Env:** Environment variables passed to OPA policies.
- **Synchronization Mechanisms:** Ensures thread-safe operations using mutex locks.

### Core Methods

- **`NewAgent(opts AgentOpts) *Agent`**

  Initializes a new `Agent` with the provided options, which include the Object Store, logger, and environment variables.

- **`SetBundle(name string) error`**

  Loads a policy bundle from the Object Store into the agent. It locks the agent during the update to ensure thread safety.

- **`WatchBundleUpdates(name string)`**

  Watches for updates to a specific bundle in the Object Store. When an update is detected, the agent reloads the bundle.

- **`Eval(ctx context.Context, input []byte, pkg string) ([]byte, error)`**

  Evaluates a policy with the given input and package path. It returns the result as a byte slice, which can be unmarshaled into the appropriate type.

### Usage Example

Here's how you might use the `Agent` in your application:

1. **Initialize the Agent:**

   ```go
   agent := nopa.NewAgent(nopa.AgentOpts{
       Store:  objStore,
       Logger: logger,
       Env:    map[string]string{"ENV_VAR": "value"},
   })
   ```

2. **Load a Bundle:**

   ```go
   err := agent.SetBundle("bundle-name")
   if err != nil {
       // Handle error
   }
   ```

3. **Evaluate a Policy:**

   ```go
   inputData := map[string]interface{}{
       "key": "value",
   }
   inputBytes, _ := json.Marshal(inputData)

   resultBytes, err := agent.Eval(context.Background(), inputBytes, "data.package.policy")
   if err != nil {
       // Handle error
   }

   // Process the result
   var result interface{}
   json.Unmarshal(resultBytes, &result)
   ```

4. **Watch for Bundle Updates:**

   ```go
   go agent.WatchBundleUpdates("bundle-name")
   ```

## Testing

A unit test (`nopa_test.go`) demonstrates how to set up an in-memory NATS server, create an Object Store, store a policy bundle, and evaluate a policy using the `Agent`.

### Key Steps in the Test

- **Starting an In-Memory NATS Server:**

  The test initializes a NATS server with JetStream enabled, running on a random available port to avoid conflicts.

- **Creating an Object Store:**

  An Object Store named "test-store" is created within the JetStream context to store the policy bundle.

- **Preparing and Storing the Policy Bundle:**

  - A sample OPA policy is defined in Rego language.
  - The policy is parsed into an AST module.
  - A bundle is created containing the policy module and an empty data object.
  - The bundle is serialized into a tarball and stored in the Object Store.

- **Loading the Bundle into the Agent:**

  The `Agent` loads the policy bundle from the Object Store using the `SetBundle` method.

- **Evaluating the Policy:**

  - Input data is prepared and marshaled into JSON.
  - The `Eval` method is called to evaluate the policy with the given input.
  - The result is unmarshaled and checked to ensure the policy behaves as expected.

### Simplified Test Code Structure

```go
func TestAgent_Eval(t *testing.T) {
    // Setup in-memory NATS server and Object Store
    // Initialize the Agent
    // Prepare and store the policy bundle
    // Load the bundle into the Agent
    // Prepare input data
    // Evaluate the policy
    // Check the result
}
```

## Notes

- **Customization:** How you generate the package name and input is up to your application logic. You might use the NATS message subject as the package name.

- **Thread Safety:** The `Agent` uses mutex locks to ensure thread-safe access to the bundle during evaluations and updates.

- **Environment Variables:** Use the `Env` map to pass environment variables to your OPA policies, accessible via the `opa.runtime()` function in Rego.

## License

This project is licensed under the Apache License, Version 2.0. See the [LICENSE](LICENSE) file for details.
