import dagre from 'dagre';
import type { DagGraph, LayoutResult, LayoutNode, LayoutEdge } from './types';

const NODE_WIDTH = 180;
const NODE_HEIGHT = 60;

export function layoutDag(graph: DagGraph): LayoutResult {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: 'TB', ranksep: 60, nodesep: 40 });
  g.setDefaultEdgeLabel(() => ({}));

  for (const node of graph.nodes) {
    g.setNode(node.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
  }

  for (const edge of graph.edges) {
    g.setEdge(edge.source, edge.target);
  }

  dagre.layout(g);

  const nodes: LayoutNode[] = graph.nodes.map((node) => {
    const n = g.node(node.id);
    return {
      ...node,
      x: n.x,
      y: n.y,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    };
  });

  const edges: LayoutEdge[] = graph.edges.map((edge) => {
    const e = g.edge(edge.source, edge.target);
    return {
      ...edge,
      points: e.points,
    };
  });

  const gGraph = g.graph();
  return {
    nodes,
    edges,
    width: (gGraph.width ?? 400) + 40,
    height: (gGraph.height ?? 300) + 40,
  };
}
