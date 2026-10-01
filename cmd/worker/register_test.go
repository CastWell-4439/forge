package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/worker"
)

// workflowWorkerRefs mirrors just the part of the workflow schema this guard
// needs: every task names the worker that will execute it.
type workflowWorkerRefs struct {
	Stages []struct {
		Tasks []struct {
			Worker string `yaml:"worker"`
		} `yaml:"tasks"`
	} `yaml:"stages"`
}

// TestEveryWorkflowWorkerIsRegistered keeps the dispatch chain complete.
//
// A workflow that declares a worker this binary does not register can only fail
// with "unknown handler" at run time, and only on the node that needed it. This
// guard turns that into a build-time failure. It is the worker-registry
// counterpart of the golden tool list that protects the agent's tool registry.
func TestEveryWorkflowWorkerIsRegistered(t *testing.T) {
	registry := worker.NewRegistry()
	registerBuiltinHandlers(registry)

	registered := make(map[string]bool, len(registry.Handlers()))
	for _, name := range registry.Handlers() {
		registered[name] = true
	}

	paths, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no workflow files found; the guard would silently pass")
	}

	referenced := make(map[string]bool)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var wf workflowWorkerRefs
		if err := yaml.Unmarshal(data, &wf); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, stage := range wf.Stages {
			for _, task := range stage.Tasks {
				if task.Worker == "" {
					continue
				}
				referenced[task.Worker] = true
				if !registered[task.Worker] {
					t.Errorf("%s declares worker %q, which the worker binary does not register",
						filepath.Base(path), task.Worker)
				}
			}
		}
	}

	if len(referenced) == 0 {
		t.Fatal("no worker references found; the guard would silently pass")
	}
	t.Logf("workflows reference %d workers; registered: %v", len(referenced), registry.Handlers())
}

// TestWorkflowWorkersCoverTheDocumentedSet pins the worker set the workflows are
// written against, so dropping one from registerBuiltinHandlers is a deliberate
// act rather than a silent regression.
func TestWorkflowWorkersCoverTheDocumentedSet(t *testing.T) {
	registry := worker.NewRegistry()
	registerBuiltinHandlers(registry)
	registered := make(map[string]bool, len(registry.Handlers()))
	for _, name := range registry.Handlers() {
		registered[name] = true
	}

	for _, want := range []string{"ai", "review", "database", "git", "mcp", "hitl", "shell", "claude_code"} {
		if !registered[want] {
			t.Errorf("worker %q is not registered", want)
		}
	}
}

// TestKnowledgeStackIsUsable exercises the review worker's retrieval pipeline
// end to end with no endpoints configured: index a document, search it back,
// and confirm the pipeline reports its honest mode (BM25-only) instead of
// pretending a vector pass happened.
func TestKnowledgeStackIsUsable(t *testing.T) {
	t.Setenv(envKnowledgeDir, t.TempDir())
	t.Setenv("FORGE_EMBEDDING_BASE_URL", "")
	t.Setenv("FORGE_LLM_BASE_URL", "")
	t.Setenv("FORGE_RERANK_ENDPOINT", "")

	ctx := context.Background()
	stack := buildKnowledgeStack()
	if stack == nil {
		t.Fatal("buildKnowledgeStack must always return a usable retriever")
	}

	docs := []core.Document{{ID: "d1", Content: "release checklist for the forge build"}}
	if err := stack.Index(ctx, docs); err != nil {
		t.Fatalf("index: %v", err)
	}

	hits, err := stack.Search(ctx, "release", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "d1" {
		t.Fatalf("hits = %+v, want the indexed document back", hits)
	}

	mode := stack.SearchMode()
	if !strings.HasPrefix(mode, "bm25-only") {
		t.Errorf("mode = %q, want the documented BM25-only degrade", mode)
	}
}
