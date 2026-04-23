export interface DagNode {
  id: string;
  label: string;
  handler: string;
  status: string;
}

export interface DagEdge {
  source: string;
  target: string;
}

export interface DagGraph {
  nodes: DagNode[];
  edges: DagEdge[];
}

export interface LayoutNode extends DagNode {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface LayoutEdge extends DagEdge {
  points: { x: number; y: number }[];
}

export interface LayoutResult {
  nodes: LayoutNode[];
  edges: LayoutEdge[];
  width: number;
  height: number;
}
