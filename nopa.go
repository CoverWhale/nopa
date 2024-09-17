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
	"github.com/open-policy-agent/opa/rego"
)

var (
	ErrNotFound error = fmt.Errorf("package not found")
)

type Agent struct {
	Store        nats.ObjectStore
	mutex        sync.RWMutex
	Logger       *logr.Logger
	Env          map[string]string
	astFunc      func(*rego.Rego)
	bundleLoader func(*rego.Rego)
}

type AgentOpts struct {
	Store  nats.ObjectStore
	Logger *logr.Logger
	Env    map[string]string
}

func NewAgent(opts AgentOpts) *Agent {
	a := &Agent{
		Store:  opts.Store,
		Logger: opts.Logger,
		Env:    opts.Env,
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

func (a *Agent) SetBundle(name string) error {
	a.Logger.Info("locking requests to update bundle")
	a.mutex.Lock()

	// get bundle from NATS object bucket
	f, err := a.Store.Get(name)
	if err != nil {
		return fmt.Errorf("error getting object %v", err)
	}

	// build new reader from tarball retrieved over NATS
	tarball := bundle.NewCustomReader(bundle.NewTarballLoaderWithBaseURL(f, ""))
	b, err := tarball.Read()
	if err != nil {
		return fmt.Errorf("error reading bundle: %v", err)
	}

	// generate bundle from tarball file
	a.bundleLoader = rego.ParsedBundle("cw", &b)
	a.Logger.Info("unlocking requests")
	a.mutex.Unlock()

	return nil
}

func (a *Agent) WatchBundleUpdates(name string) {
	watcher, err := a.Store.Watch(nats.IgnoreDeletes())
	if err != nil {
		a.Logger.Error(err)
	}

	for v := range watcher.Updates() {
		if v == nil {
			continue
		}

		if v.Name != name {
			continue
		}

		a.SetBundle(v.Name)
	}
}

func (a *Agent) Eval(ctx context.Context, input []byte, pkg string) ([]byte, error) {
	if a.bundleLoader == nil {
		return nil, fmt.Errorf("bundle not loaded")
	}

	if input == nil {
		return nil, fmt.Errorf("input required")
	}

	if pkg == "" {
		return nil, fmt.Errorf("package name required")
	}

	var data map[string]interface{}

	if err := json.Unmarshal(input, &data); err != nil {
		return nil, err
	}

	a.mutex.RLock()
	r, err := rego.New(
		rego.Query(fmt.Sprintf("x = %s", pkg)),
		a.astFunc,
		a.bundleLoader,
	).PrepareForEval(ctx)
	if err != nil {
		return nil, err
	}

	results, err := r.Eval(ctx, rego.EvalInput(data))
	if err != nil {
		return nil, err
	}

	if len(results) < 1 {
		return nil, ErrNotFound
	}

	a.mutex.RUnlock()
	return json.Marshal(results[0].Bindings["x"])

}
