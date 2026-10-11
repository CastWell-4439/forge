package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refs is a shorthand for building input.
func refs(ids ...string) []SessionRef {
	out := make([]SessionRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, SessionRef{ID: id, State: "completed"})
	}
	return out
}

// idsOf lists the ids of a node slice.
func idsOf(nodes []*DelegationNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Ref.ID)
	}
	return out
}

// A chain resolves to a chain: each node's parent is the id it was derived from.
func TestProjectionBuildsAChain(t *testing.T) {
	tree := BuildDelegationTree(refs("s1", "s1-sub-1", "s1-sub-1-sub-2"))

	require.Equal(t, 3, tree.Size())
	require.Len(t, tree.Roots, 1)
	assert.Equal(t, "s1", tree.Roots[0].Ref.ID)

	child := tree.Find("s1-sub-1")
	require.NotNil(t, child)
	assert.Equal(t, "s1", child.ParentID)
	assert.Equal(t, 1, child.Depth)
	assert.Equal(t, 1, child.Seq)
	assert.False(t, child.Orphan)

	grandchild := tree.Find("s1-sub-1-sub-2")
	require.NotNil(t, grandchild)
	assert.Equal(t, "s1-sub-1", grandchild.ParentID, "the parent is the whole prefix, not the root")
	assert.Equal(t, 2, grandchild.Depth)
	assert.Equal(t, 2, grandchild.Seq)

	assert.Equal(t, 2, tree.MaxDepth())
}

// Input order must not matter: parent and child may arrive either way round,
// because a store's ordering is not the caller's to control.
func TestProjectionIsOrderIndependent(t *testing.T) {
	forward := BuildDelegationTree(refs("s1", "s1-sub-1", "s1-sub-1-sub-2"))
	backward := BuildDelegationTree(refs("s1-sub-1-sub-2", "s1-sub-1", "s1"))

	assert.Equal(t, idsOf(forward.Flatten()), idsOf(backward.Flatten()))
	assert.Equal(t, forward.MaxDepth(), backward.MaxDepth())
}

// Siblings are ordered by their NUMBER, not their id.
//
// This is the case a string sort gets wrong: "-sub-10" < "-sub-2" as text, so an
// id-ordered list would show the tenth child second. It is why Seq is parsed.
func TestSiblingsAreOrderedNumerically(t *testing.T) {
	tree := BuildDelegationTree(refs(
		"s1", "s1-sub-10", "s1-sub-2", "s1-sub-1",
	))

	root := tree.Find("s1")
	require.NotNil(t, root)
	require.Len(t, root.Children, 3)

	assert.Equal(t, []string{"s1-sub-1", "s1-sub-2", "s1-sub-10"}, idsOf(root.Children),
		"children come back by sequence, not by text")
}

// A node whose parent is not in the input is an orphan: shown, attached at the
// root, and marked. Dropping it would hide a session that really existed.
func TestOrphanIsKeptAndMarked(t *testing.T) {
	tree := BuildDelegationTree(refs("s1-sub-1"))

	node := tree.Find("s1-sub-1")
	require.NotNil(t, node)
	assert.True(t, node.Orphan)
	assert.Equal(t, "s1", node.ParentID, "the parent it named is recorded even though it is missing")
	assert.Equal(t, 1, node.Depth, "it is one level below the parent it named")

	require.Len(t, tree.Orphans(), 1)
	assert.Equal(t, "s1-sub-1", tree.Orphans()[0].Ref.ID)

	// And it is still reachable in the flat view.
	assert.Contains(t, idsOf(tree.Flatten()), "s1-sub-1")
}

// Two runs produce two trees. Merging them under one root would draw a
// relationship that does not exist.
func TestSeparateRootsStaySeparate(t *testing.T) {
	tree := BuildDelegationTree(refs(
		"run-a", "run-a-sub-1",
		"run-b", "run-b-sub-1",
	))

	require.Len(t, tree.Roots, 2)
	assert.Equal(t, []string{"run-a", "run-b"}, idsOf(tree.Roots))

	assert.Equal(t, "run-a", tree.Find("run-a-sub-1").ParentID)
	assert.Equal(t, "run-b", tree.Find("run-b-sub-1").ParentID)
}

// An id with no lineage is a root, and one with a malformed suffix is a root
// too: treating "s1-sub-x" as a child would invent a relationship from a typo.
func TestMalformedIdsAreRoots(t *testing.T) {
	for _, id := range []string{
		"s1",            // no suffix at all
		"s1-sub-x",      // suffix is not a number
		"-sub-1",        // nothing before the separator
		"s1-sub-",       // nothing after it
		"s1-sub-1-sub-", // the last link is malformed, so the node is a root
	} {
		t.Run(id, func(t *testing.T) {
			tree := BuildDelegationTree(refs(id))
			node := tree.Find(id)
			require.NotNil(t, node)
			assert.Empty(t, node.ParentID, "a malformed id has no parent")
			assert.False(t, node.Orphan, "it is a root, not an orphan: it named no parent")
			assert.Equal(t, 0, node.Depth)
			assert.Len(t, tree.Roots, 1)
		})
	}
}

// A malformed link earlier in the chain still leaves the later ones intact.
func TestMalformedLinkEndsTheLineage(t *testing.T) {
	// "s1-sub-x-sub-2": the LAST link is valid, so this node's parent is
	// "s1-sub-x" — which is itself a root because its own link is malformed.
	tree := BuildDelegationTree(refs("s1-sub-x", "s1-sub-x-sub-2"))

	child := tree.Find("s1-sub-x-sub-2")
	require.NotNil(t, child)
	assert.Equal(t, "s1-sub-x", child.ParentID)
	assert.False(t, child.Orphan, "the parent it named IS in the input")
	assert.Equal(t, 1, child.Depth)

	parent := tree.Find("s1-sub-x")
	assert.Empty(t, parent.ParentID, "the malformed link stops the chain here")
	assert.Equal(t, 0, parent.Depth)
}

// Flatten puts a parent before its children, which is what makes the flat form
// usable without rebuilding the structure.
func TestFlattenPutsParentsFirst(t *testing.T) {
	tree := BuildDelegationTree(refs(
		"s1", "s1-sub-1", "s1-sub-1-sub-1", "s1-sub-2",
	))

	flat := idsOf(tree.Flatten())
	require.Len(t, flat, 4)

	pos := map[string]int{}
	for i, id := range flat {
		pos[id] = i
	}
	assert.Less(t, pos["s1"], pos["s1-sub-1"])
	assert.Less(t, pos["s1-sub-1"], pos["s1-sub-1-sub-1"])
	assert.Less(t, pos["s1"], pos["s1-sub-2"])
}

// A duplicate id is one session reported twice, not two sessions.
func TestDuplicateIdsCollapse(t *testing.T) {
	tree := BuildDelegationTree([]SessionRef{
		{ID: "s1", State: "running"},
		{ID: "s1", State: "completed"},
	})

	assert.Equal(t, 1, tree.Size())
	assert.Len(t, tree.Roots, 1)
	assert.Equal(t, "running", tree.Roots[0].Ref.State, "the first report wins")
}

// An empty input is an empty forest, not a nil panic.
func TestEmptyInput(t *testing.T) {
	tree := BuildDelegationTree(nil)
	require.NotNil(t, tree)
	assert.Equal(t, 0, tree.Size())
	assert.Empty(t, tree.Roots)
	assert.Empty(t, tree.Flatten())
	assert.Equal(t, 0, tree.MaxDepth())
	assert.Nil(t, tree.Find("anything"))
}

// A blank id is skipped rather than becoming a nameless root.
func TestBlankIdsAreSkipped(t *testing.T) {
	tree := BuildDelegationTree([]SessionRef{{ID: ""}, {ID: "   "}, {ID: "s1"}})

	assert.Equal(t, 1, tree.Size())
	assert.Equal(t, "s1", tree.Roots[0].Ref.ID)
}

// Deep chains work, and MaxDepth reports the real depth — the same quantity the
// delegation limit counts, so a configuration can be checked against what
// happened.
func TestDeepChainAndMaxDepth(t *testing.T) {
	id := "root"
	ids := []string{id}
	for i := 1; i <= 10; i++ {
		id = id + "-sub-1"
		ids = append(ids, id)
	}

	tree := BuildDelegationTree(refs(ids...))

	assert.Equal(t, 11, tree.Size())
	assert.Equal(t, 10, tree.MaxDepth())

	deepest := tree.Find(id)
	require.NotNil(t, deepest)
	assert.Equal(t, 10, deepest.Depth)
	assert.Empty(t, deepest.Children)
}

// Render indents by depth and carries the caller's label, so the projection
// does not decide what a node is called.
func TestRenderIndentsAndLabels(t *testing.T) {
	tree := BuildDelegationTree([]SessionRef{
		{ID: "s1", State: "running"},
		{ID: "s1-sub-1", State: "completed"},
	})

	lines := tree.Render(func(n *DelegationNode) string { return n.Ref.State })
	require.Len(t, lines, 2)
	assert.Equal(t, "s1  running", lines[0])
	assert.Equal(t, "  s1-sub-1  completed", lines[1], "a child is indented one level")
}

// An orphan's line says so, and names the parent that was missing: a reader
// looking at a disconnected session needs to know it is disconnected and why.
func TestRenderMarksOrphans(t *testing.T) {
	tree := BuildDelegationTree(refs("s1-sub-1"))

	lines := tree.Render(nil)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "s1-sub-1")
	assert.Contains(t, lines[0], "orphan")
	assert.Contains(t, lines[0], "s1", "the missing parent is named")
}

// Render tolerates a nil label, so a caller that wants only the structure does
// not have to pass a no-op.
func TestRenderWithoutLabel(t *testing.T) {
	tree := BuildDelegationTree(refs("s1"))
	lines := tree.Render(nil)
	require.Len(t, lines, 1)
	assert.Equal(t, "s1", strings.TrimSpace(lines[0]))
}

// The tree never writes: it must not be the second place that decides what a
// child id looks like. Reading the same input twice gives the same projection.
func TestProjectionIsRepeatable(t *testing.T) {
	input := refs("s1", "s1-sub-1", "s1-sub-2", "s1-sub-1-sub-1")

	first := idsOf(BuildDelegationTree(input).Flatten())
	second := idsOf(BuildDelegationTree(input).Flatten())
	assert.Equal(t, first, second)
}
