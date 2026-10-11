package harness

import (
	"sort"
	"strconv"
	"strings"
)

// The delegation tree, projected from session ids.
//
// A delegation's lineage has always been recoverable from its id alone:
// defaultChildID keeps the parent as a prefix, so "s1-sub-2-sub-1" says "the
// first child of the second child of s1" without consulting any registry. That
// was the point of the convention, and it still holds.
//
// What it does not give is the other direction. Answering "which children did
// this run spawn" or "what does the whole tree look like" means reading every
// session and parsing ids — and nothing in the system did that, so the shape of
// a delegation was only ever visible one node at a time, from the child
// upwards.
//
// This file is that missing view. It is a PROJECTION, not a new source of
// truth: it reads ids and reports structure, and it stores nothing. Three
// consequences follow, and all three are deliberate:
//
//   - No schema changes. Session has no parent field and the store has no
//     parent query, and neither is added: the information is already in the id,
//     and a field would be absent for every session recorded before it existed.
//   - It works on history. Any session id ever written can be projected,
//     including ones created before this file existed.
//   - It cannot go stale. A projection derived on demand cannot disagree with
//     the ids it is derived from; a persisted tree could, and would.

// childIDSeparator and the numeric suffix after it are the convention
// defaultChildID writes. They are read here, never written: this file must not
// be the second place that decides what a child id looks like.
const childIDSeparator = "-sub-"

// SessionRef is the minimum a projection needs to know about a session.
//
// It is deliberately not *session.Session: the harness is a lower layer than
// the session package, so importing it would point the dependency the wrong way.
// Callers map their own type into this one.
type SessionRef struct {
	// ID is the session id, which carries the lineage.
	ID string
	// State is whatever the caller wants shown (a session state, a run status).
	// The projection never interprets it.
	State string
	// StartedAt is optional ordering information for siblings that share a seq,
	// which should not happen but is cheap to make deterministic.
	StartedAt string
}

// DelegationNode is one session in the projected tree.
type DelegationNode struct {
	// Ref is what the caller supplied.
	Ref SessionRef
	// ParentID is the id this node was derived from, or "" for a root.
	ParentID string
	// Depth is how many delegation levels down this node is: a root is 0, its
	// child 1, and so on. It is the same quantity the depth limit counts, so the
	// two can be checked against each other.
	Depth int
	// Seq is the child's position among its parent's children, as written in the
	// id. Zero for a root.
	Seq int
	// Children are the nodes that named this one as their parent, ordered by Seq.
	Children []*DelegationNode
	// Orphan reports that this node's id named a parent that is not in the
	// input: a delegation whose parent session was not listed (deleted, from
	// another store, or simply not queried).
	//
	// An orphan is still shown, attached at the root. Dropping it would hide a
	// session that really existed, and the point of the view is to show what is
	// there — including the parts that do not connect.
	Orphan bool
}

// DelegationTree is a projected forest.
//
// It is a forest rather than a tree because a run's sessions and another run's
// are separate: merging them under one synthetic root would draw a relationship
// that does not exist.
type DelegationTree struct {
	// Roots are the top-level nodes, ordered by id.
	Roots []*DelegationNode
	// byID indexes every node, including orphans, so Find answers for any input.
	byID map[string]*DelegationNode
}

// BuildDelegationTree projects a forest from session references.
//
// The input order does not matter: parent and child may appear in either order,
// because ids are resolved after every node exists rather than as they are read.
// That is what lets a caller pass whatever order its store returned.
func BuildDelegationTree(refs []SessionRef) *DelegationTree {
	tree := &DelegationTree{byID: make(map[string]*DelegationNode, len(refs))}

	// First pass: one node per distinct id. A duplicate id is the same session
	// reported twice, and keeping the first is the only answer that does not
	// invent a second session.
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			continue
		}
		if _, seen := tree.byID[id]; seen {
			continue
		}
		tree.byID[id] = &DelegationNode{Ref: SessionRef{ID: id, State: ref.State, StartedAt: ref.StartedAt}}
	}

	// Second pass: resolve each node's parent by parsing its id.
	//
	// A node whose parent id is not present becomes an orphan at the root. It is
	// NOT an error: the parent may simply not have been queried, and failing the
	// whole projection over a partial listing would make the view useless
	// exactly when a caller wants to look at part of a tree.
	for id, node := range tree.byID {
		parentID, seq, ok := parseChildID(id)
		if !ok {
			node.Depth = 0
			continue
		}
		node.ParentID = parentID
		node.Seq = seq
		parent, found := tree.byID[parentID]
		if !found {
			node.Orphan = true
			node.Depth = 1 // one level below the parent it named, wherever that is
			continue
		}
		parent.Children = append(parent.Children, node)
	}

	// Order siblings by their numeric seq, not their id.
	//
	// "-sub-10" sorts before "-sub-2" as a string, so an id-ordered list would
	// put the tenth child second. This is the whole reason Seq is parsed rather
	// than compared as text.
	var order func(node *DelegationNode, depth int)
	order = func(node *DelegationNode, depth int) {
		node.Depth = depth
		sort.SliceStable(node.Children, func(i, j int) bool {
			if node.Children[i].Seq != node.Children[j].Seq {
				return node.Children[i].Seq < node.Children[j].Seq
			}
			return node.Children[i].Ref.ID < node.Children[j].Ref.ID
		})
		for _, child := range node.Children {
			order(child, depth+1)
		}
	}

	// Collect roots: nodes with no parent, plus orphans. Both are attached at
	// depth 0 in the forest, but only the orphan is marked — "no parent" and
	// "a parent I could not find" are different facts about a session.
	for _, node := range tree.byID {
		if node.ParentID == "" || node.Orphan {
			tree.Roots = append(tree.Roots, node)
		}
	}
	sort.Slice(tree.Roots, func(i, j int) bool { return tree.Roots[i].Ref.ID < tree.Roots[j].Ref.ID })
	for _, root := range tree.Roots {
		// An orphan's depth stays 1 (it named a parent); a real root is 0. The
		// walk below would reset it, so orphans are walked from their own depth.
		start := 0
		if root.Orphan {
			start = 1
		}
		order(root, start)
	}

	return tree
}

// parseChildID splits an id into its parent and sequence.
//
// It reads from the RIGHT: the last "-sub-N" is this node's own link, and
// everything before it is the parent id — which may itself end in "-sub-M".
// Reading from the left would mis-parse a chain, and reading the first separator
// would make every grandchild a child of the root.
func parseChildID(id string) (parent string, seq int, ok bool) {
	idx := strings.LastIndex(id, childIDSeparator)
	if idx <= 0 {
		return "", 0, false
	}
	parent = id[:idx]
	if parent == "" {
		return "", 0, false
	}
	rawSeq := id[idx+len(childIDSeparator):]
	n, err := strconv.Atoi(rawSeq)
	if err != nil {
		// A malformed suffix is not a lineage. Treating "s1-sub-x" as a child
		// would invent a relationship from a typo.
		return "", 0, false
	}
	return parent, n, true
}

// Find returns the node for an id, or nil.
func (t *DelegationTree) Find(id string) *DelegationNode {
	if t == nil {
		return nil
	}
	return t.byID[strings.TrimSpace(id)]
}

// Size is how many sessions the projection holds.
func (t *DelegationTree) Size() int {
	if t == nil {
		return 0
	}
	return len(t.byID)
}

// Orphans returns the nodes whose parent was not in the input, ordered by id.
func (t *DelegationTree) Orphans() []*DelegationNode {
	if t == nil {
		return nil
	}
	var out []*DelegationNode
	for _, node := range t.byID {
		if node.Orphan {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.ID < out[j].Ref.ID })
	return out
}

// Flatten returns every node in a deterministic order: roots by id, each
// followed by its subtree depth-first, children by seq.
//
// A parent always precedes its children, which is what makes the flat form
// usable for display without the caller rebuilding the structure.
func (t *DelegationTree) Flatten() []*DelegationNode {
	if t == nil {
		return nil
	}
	out := make([]*DelegationNode, 0, len(t.byID))
	var walk func(node *DelegationNode)
	walk = func(node *DelegationNode) {
		out = append(out, node)
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range t.Roots {
		walk(root)
	}
	return out
}

// MaxDepth reports the deepest level in the forest.
//
// It is the same quantity the delegation limit counts, so a caller can check a
// configuration against what actually happened rather than against what was
// intended.
func (t *DelegationTree) MaxDepth() int {
	deepest := 0
	for _, node := range t.Flatten() {
		if node.Depth > deepest {
			deepest = node.Depth
		}
	}
	return deepest
}

// Render draws the forest as indented lines, for a log or a CLI.
//
// It lives here rather than in a caller because the shape is the projection's
// business: a second implementation would be a second opinion about what the
// tree looks like. The caller supplies the label so the projection does not
// decide what a node is called.
func (t *DelegationTree) Render(label func(*DelegationNode) string) []string {
	if t == nil {
		return nil
	}
	var out []string
	for _, node := range t.Flatten() {
		indent := strings.Repeat("  ", node.Depth)
		line := indent + node.Ref.ID
		// A nil label means "structure only", which is a legitimate request; it
		// must not be a panic. Callers that want a label pass one.
		if label != nil {
			if extra := label(node); extra != "" {
				line += "  " + extra
			}
		}
		if node.Orphan {
			line += "  (orphan: parent " + node.ParentID + " not listed)"
		}
		out = append(out, line)
	}
	return out
}
