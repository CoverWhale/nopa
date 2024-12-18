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
	"github.com/open-policy-agent/opa/topdown/cache"
)

var (
	ErrNotFound error = fmt.Errorf("package not found")
)

// BundleModifyFunc will take a bundle and allow for modifications
// like adding custom modules
type BundleModifyFunc func(b bundle.Bundle) (bundle.Bundle, error)

type Agent struct {
	BundleName  string
	ObjectStore nats.ObjectStore
	OPAStore    storage.Store
	mutex       sync.RWMutex
	Logger      *logr.Logger
	Env         map[string]string
	astFunc     func(*rego.Rego)
	Compiler    *ast.Compiler
	Modifiers   []BundleModifyFunc
	Cache       cache.InterQueryCache
}

type AgentOpts struct {
	BundleName  string
	ObjectStore nats.ObjectStore
	Logger      *logr.Logger
	Env         map[string]string
	Modifiers   []BundleModifyFunc
}

func NewAgent(opts AgentOpts) *Agent {
	config, _ := cache.ParseCachingConfig(nil)
	interQueryCache := cache.NewInterQueryCache(config)
	a := &Agent{
		BundleName:  opts.BundleName,
		ObjectStore: opts.ObjectStore,
		Logger:      opts.Logger,
		Env:         opts.Env,
		OPAStore:    inmem.New(),
		Compiler:    ast.NewCompiler(),
		Modifiers:   opts.Modifiers,
		Cache:       cache.InterQueryCache(interQueryCache),
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
	ok := a.mutex.TryLock()
	if !ok {
		a.mutex.Unlock()
		a.mutex.Lock()
	}
	a.Logger.Info("locked successfully")
	defer func() {
		a.Logger.Info("unlocking requests")
		a.mutex.Unlock()
		a.Logger.Info("unlocked successfully")
	}()

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

	for _, v := range a.Modifiers {
		a.Logger.Debug("modifying bundle")
		b, err = v(b)
		if err != nil {
			return fmt.Errorf("error in bundle modifier: %w", err)
		}
	}

	if err := a.Activate(ctx, b); err != nil {
		return err
	}
	a.Logger.Info("activated bundle successfully")

	return nil
}

func (a *Agent) WatchBundleUpdates(errChan chan<- error) {
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

		if err := a.SetBundle(v.Name); err != nil {
			err = fmt.Errorf("error setting bundle: %w", err)
			a.Logger.Error(err)
			errChan <- err
		}
	}
}

func (a *Agent) MustWatchBundleUpdates() {
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

		if err := a.SetBundle(v.Name); err != nil {
			a.Logger.Panicf("error setting bundle: %v", err)
		}
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

	a.Logger.Infof("evaluating package: %s", pkg)
	a.Logger.Debugf("parsing input: %v", string(input))
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
		rego.InterQueryBuiltinCache(a.Cache),
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
		rego.EvalInterQueryBuiltinCache(a.Cache),
	)
	if err != nil {
		a.Logger.Error(err)
		return nil, err
	}

	if len(results) < 1 {
		return nil, ErrNotFound
	}

	a.mutex.RUnlock()
	value, err := json.Marshal(results[0].Expressions[0].Value)
	if err != nil {
		return nil, err
	}

	a.Logger.Debugf("response: %s", string(value))

	return value, nil
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
		a.OPAStore.Abort(ctx, txn)
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
