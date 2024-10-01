package nopa

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/CoverWhale/logr"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/bundle"
	"github.com/stretchr/testify/require"
)

func TestAgent_Eval(t *testing.T) {
	// Start a local NATS server with JetStream enabled
	opts := &server.Options{
		Port:      -1, // Random available port
		JetStream: true,
	}
	natsServer := test.RunServer(opts)
	defer natsServer.Shutdown()

	// Connect to the NATS server
	nc, err := nats.Connect(natsServer.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	// Create a JetStream context
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Create an Object Store
	storeName := "test-store"
	objStore, err := js.CreateObjectStore(&nats.ObjectStoreConfig{
		Bucket: storeName,
	})
	require.NoError(t, err)

	// Create a logger
	logger := &logr.Logger{}

	// Create an Agent
	agent := NewAgent(AgentOpts{
		Store:  objStore,
		Logger: logger,
		Env:    map[string]string{"ENV_VAR": "test_value"},
	})

	// Prepare a sample policy
	samplePolicy := `
    package example

    default allow = false

    allow {
        input.user == "admin"
    }
    `

	// Parse the module
	module, err := ast.ParseModule("example.rego", samplePolicy)
	require.NoError(t, err)

	// Create a bundle with an empty data object
	bundleName := "test-bundle"
	b := &bundle.Bundle{
		Data: map[string]interface{}{}, // Include an empty data object
		Modules: []bundle.ModuleFile{
			{
				URL:    "example.rego",
				Path:   "example.rego",
				Raw:    []byte(samplePolicy),
				Parsed: module,
			},
		},
	}

	// Serialize the bundle into a tarball
	var buf bytes.Buffer
	err = bundle.NewWriter(&buf).Write(*b)
	require.NoError(t, err)

	// Store the bundle in the Object Store
	_, err = objStore.Put(&nats.ObjectMeta{Name: bundleName}, bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)

	// Load the bundle into the Agent
	err = agent.SetBundle(bundleName)
	require.NoError(t, err)

	// Prepare input data
	inputData := map[string]interface{}{
		"user": "admin",
	}
	inputBytes, err := json.Marshal(inputData)
	require.NoError(t, err)

	// Evaluate the policy
	ctx := context.Background()
	resultBytes, err := agent.Eval(ctx, inputBytes, "data.example.allow")
	require.NoError(t, err)

	// Parse the result
	var result bool
	err = json.Unmarshal(resultBytes, &result)
	require.NoError(t, err)

	// Check the result
	if !result {
		t.Errorf("Expected allow to be true for user 'admin', got false")
	}
}
