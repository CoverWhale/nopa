// Copyright 2024 Cover Whale Insurance Solutions Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nopa

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/CoverWhale/logr"
	"github.com/nats-io/nats.go"
	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/bundle"
	"github.com/open-policy-agent/opa/metrics"
	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/storage"
	"github.com/open-policy-agent/opa/storage/inmem"
)

var (
	ErrNotFound error = fmt.Errorf("package not found")
)

type Agent struct {
	BundleName  string
	ObjectStore nats.ObjectStore
	OPAStore    storage.Store
	mutex       sync.RWMutex
	Logger      *logr.Logger
	Env         map[string]string
	astFunc     func(*rego.Rego)
	Compiler    *ast.Compiler
}

type AgentOpts struct {
	BundleName  string
	ObjectStore nats.ObjectStore
	Logger      *logr.Logger
	Env         map[string]string
}

func NewAgent(opts AgentOpts) *Agent {
	a := &Agent{
		BundleName:  opts.BundleName,
		ObjectStore: opts.ObjectStore,
		Logger:      opts.Logger,
		Env:         opts.Env,
		OPAStore:    inmem.New(),
		Compiler:    ast.NewCompiler(),
	}
	if opts.Env != nil {
		a.SetRuntime()
	}

	return a
}

func (a *Agent) SetRuntime() {
	obj := ast.NewObject()
	env := ast.NewObject()
	for k, v := range a.Env {
		env.Insert(ast.StringTerm(k), ast.StringTerm(v))
	}
	obj.Insert(ast.StringTerm("env"), ast.NewTerm(env))
	a.astFunc = rego.Runtime(obj.Get(ast.StringTerm("env")))
}

// SetBundle updates the in-memory store with the bundle retrieved from the NATS object store
func (a *Agent) SetBundle(name string) error {
	ctx := context.Background()
	a.Logger.Info("locking requests to update bundle")
	a.mutex.Lock()
	a.Logger.Info("locked successfully")

	// get bundle from NATS object bucket
	f, err := a.ObjectStore.Get(name)
	if err != nil {
		return fmt.Errorf("error getting object %v", err)
	}
	a.Logger.Info("retrieved bundle from object store")

	// build new reader from tarball retrieved over NATS
	tarball := bundle.NewCustomReader(bundle.NewTarballLoaderWithBaseURL(f, ""))
	b, err := tarball.Read()
	if err != nil {
		return fmt.Errorf("error reading bundle: %v", err)
	}
	a.Logger.Info("generated tarball from bundle successfully")

	if err := a.Activate(ctx, b); err != nil {
		return err
	}

	a.Logger.Info("activated bundle successfully")
	a.Logger.Info("unlocking requests")
	a.mutex.Unlock()
	a.Logger.Info("unlocked successfully")

	return nil
}

func (a *Agent) WatchBundleUpdates() {
	watcher, err := a.ObjectStore.Watch(nats.IgnoreDeletes())
	if err != nil {
		a.Logger.Error(err)
	}

	for v := range watcher.Updates() {
		if v == nil {
			continue
		}

		if v.Name != a.BundleName {
			continue
		}

		a.SetBundle(v.Name)
	}
}

// Eval evaluates the input against the policy package
func (a *Agent) Eval(ctx context.Context, input []byte, pkg string) ([]byte, error) {
	if input == nil {
		return nil, fmt.Errorf("input required")
	}

	if pkg == "" {
		return nil, fmt.Errorf("package name required")
	}

	a.Logger.Info("parsing input")
	data, _, err := readInputGetV1(input)
	if err != nil {
		a.Logger.Error(err)
		return nil, err
	}

	a.mutex.RLock()
	c := storage.NewContext()
	txn, err := a.OPAStore.NewTransaction(ctx, storage.TransactionParams{Context: c})
	if err != nil {
		a.Logger.Error(err)
		return nil, err
	}
	defer a.OPAStore.Abort(ctx, txn)

	r := rego.New(
		rego.Compiler(a.Compiler),
		rego.Query(pkg),
		rego.Transaction(txn),
		rego.Store(a.OPAStore),
		rego.ParsedInput(data),
		a.astFunc,
	)

	prepared, err := r.PrepareForEval(ctx)
	if err != nil {
		a.Logger.Error(err)
		return nil, err
	}

	results, err := prepared.Eval(ctx,
		rego.EvalParsedInput(data),
		rego.EvalTransaction(txn),
	)
	if err != nil {
		a.Logger.Error(err)
		return nil, err
	}

	if len(results) < 1 {
		return nil, ErrNotFound
	}

	a.mutex.RUnlock()
	return json.Marshal(results[0].Expressions[0].Value)

}

func (a *Agent) Activate(ctx context.Context, b bundle.Bundle) error {
	bundles := map[string]*bundle.Bundle{
		"nopa": &b,
	}
	c := storage.NewContext()
	txn, err := a.OPAStore.NewTransaction(ctx, storage.TransactionParams{Context: c, Write: true})
	if err != nil {
		return err
	}
	opts := bundle.ActivateOpts{
		Ctx:      ctx,
		Store:    a.OPAStore,
		Bundles:  bundles,
		Txn:      txn,
		TxnCtx:   c,
		Compiler: a.Compiler,
		Metrics:  metrics.New(),
	}

	if err := bundle.Activate(&opts); err != nil {
		a.Logger.Error(err)
		return err
	}

	return a.OPAStore.Commit(ctx, txn)
}

func readInputGetV1(data []byte) (ast.Value, *interface{}, error) {
	var input interface{}
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, nil, fmt.Errorf("invalid input: %w", err)
	}
	v, err := ast.InterfaceToValue(input)
	return v, &input, err
}
