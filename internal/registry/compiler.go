package registry

import (
	"fmt"
	"time"
)

// DAGNode represents a node in the compiled DAG graph.
type DAGNode struct {
	ID        string         // unique: "stage_name.task_index" or "stage_name" for parallel
	StageName string
	TaskIndex int
	Worker    string
	Action    string
	Params    map[string]any
	Output    string
	Condition string
	Timeout   time.Duration
	Retry     *CompiledRetry
}

// DAGEdge represents a dependency edge: From must complete before To starts.
type DAGEdge struct {
	From string // node ID
	To   string // node ID
}

// DAGGraph is the compiled directed acyclic graph from workflow stages.
type DAGGraph struct {
	Nodes    []*DAGNode
	Edges    []DAGEdge
	NodeMap  map[string]*DAGNode // ID -> Node
	Workflow *CompiledWorkflow
}

// CompileDAG transforms a CompiledWorkflow into a DAGGraph.
// Stage ordering defines sequential dependencies; tasks within a parallel stage run concurrently.
func CompileDAG(cw *CompiledWorkflow) (*DAGGraph, error) {
	g := &DAGGraph{
		NodeMap:  make(map[string]*DAGNode),
		Workflow: cw,
	}

	var prevStageNodeIDs []string

	for _, stage := range cw.Stages {
		var currentStageNodeIDs []string

		for taskIdx, task := range stage.Tasks {
			nodeID := fmt.Sprintf("%s.%d", stage.Name, taskIdx)
			node := &DAGNode{
				ID:        nodeID,
				StageName: stage.Name,
				TaskIndex: taskIdx,
				Worker:    task.Worker,
				Action:    task.Action,
				Params:    task.Params,
				Output:    task.Output,
				Condition: task.Condition,
				Timeout:   task.Timeout,
				Retry:     task.Retry,
			}
			g.Nodes = append(g.Nodes, node)
			g.NodeMap[nodeID] = node
			currentStageNodeIDs = append(currentStageNodeIDs, nodeID)
		}

		// Add edges: all tasks in previous stage must complete before current stage starts.
		// For parallel stages, all tasks within are independent (no intra-stage edges).
		// For sequential stages (parallel=false), tasks within the stage are chained.
		if !stage.Parallel && len(stage.Tasks) > 1 {
			// Chain tasks sequentially within the stage
			for i := 1; i < len(stage.Tasks); i++ {
				from := fmt.Sprintf("%s.%d", stage.Name, i-1)
				to := fmt.Sprintf("%s.%d", stage.Name, i)
				g.Edges = append(g.Edges, DAGEdge{From: from, To: to})
			}
		}

		// Inter-stage edges: previous stage's exit nodes → current stage's entry nodes
		if len(prevStageNodeIDs) > 0 {
			// Determine exit nodes of previous stage
			exitNodes := prevStageNodeIDs
			// Determine entry nodes of current stage
			var entryNodes []string
			if stage.Parallel || len(stage.Tasks) == 1 {
				entryNodes = currentStageNodeIDs
			} else {
				// Sequential: only first task is entry
				entryNodes = []string{currentStageNodeIDs[0]}
			}
			for _, from := range exitNodes {
				for _, to := range entryNodes {
					g.Edges = append(g.Edges, DAGEdge{From: from, To: to})
				}
			}
		}

		// Update prevStageNodeIDs to current stage's exit nodes
		if stage.Parallel || len(stage.Tasks) == 1 {
			prevStageNodeIDs = currentStageNodeIDs
		} else {
			// Sequential: last task is exit
			prevStageNodeIDs = []string{currentStageNodeIDs[len(currentStageNodeIDs)-1]}
		}
	}

	// Validate: check for cycles (shouldn't happen with stage-based ordering, but be safe)
	if err := validateDAG(g); err != nil {
		return nil, err
	}

	return g, nil
}

// TopologicalOrder returns the nodes in valid execution order.
func (g *DAGGraph) TopologicalOrder() ([]*DAGNode, error) {
	inDegree := make(map[string]int, len(g.Nodes))
	adj := make(map[string][]string, len(g.Nodes))

	for _, n := range g.Nodes {
		inDegree[n.ID] = 0
	}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		inDegree[e.To]++
	}

	var queue []string
	for _, n := range g.Nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}

	var order []*DAGNode
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, g.NodeMap[id])
		for _, next := range adj[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(order) != len(g.Nodes) {
		return nil, fmt.Errorf("registry/compiler: cycle detected in DAG")
	}
	return order, nil
}

// validateDAG checks the graph for cycles using topological sort.
func validateDAG(g *DAGGraph) error {
	_, err := g.TopologicalOrder()
	return err
}

// ReadyNodes returns nodes whose dependencies are all satisfied.
// completedSet contains IDs of nodes that have finished execution.
func (g *DAGGraph) ReadyNodes(completedSet map[string]bool) []*DAGNode {
	// Build reverse dependency: for each node, which nodes must be done first
	deps := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		deps[e.To] = append(deps[e.To], e.From)
	}

	var ready []*DAGNode
	for _, n := range g.Nodes {
		if completedSet[n.ID] {
			continue
		}
		allDone := true
		for _, dep := range deps[n.ID] {
			if !completedSet[dep] {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, n)
		}
	}
	return ready
}
