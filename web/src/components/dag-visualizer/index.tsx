import React, { useEffect, useRef } from 'react';
import * as d3 from 'd3';
import { layoutDag } from './layout';
import type { DagGraph, LayoutNode, LayoutEdge } from './types';

const STATUS_COLORS: Record<string, string> = {
  TASK_STATUS_PENDING: '#d9d9d9',
  TASK_STATUS_READY: '#d9d9d9',
  TASK_STATUS_SCHEDULED: '#91caff',
  TASK_STATUS_RUNNING: '#1677ff',
  TASK_STATUS_COMPLETED: '#52c41a',
  TASK_STATUS_FAILED: '#ff4d4f',
  TASK_STATUS_SKIPPED: '#fadb14',
  TASK_STATUS_COMPENSATING: '#fa8c16',
};

const STATUS_TEXT_COLORS: Record<string, string> = {
  TASK_STATUS_PENDING: '#595959',
  TASK_STATUS_READY: '#595959',
  TASK_STATUS_SCHEDULED: '#003eb3',
  TASK_STATUS_RUNNING: '#ffffff',
  TASK_STATUS_COMPLETED: '#ffffff',
  TASK_STATUS_FAILED: '#ffffff',
  TASK_STATUS_SKIPPED: '#595959',
  TASK_STATUS_COMPENSATING: '#ffffff',
};

interface Props {
  graph: DagGraph;
}

const DagVisualizer: React.FC<Props> = ({ graph }) => {
  const svgRef = useRef<SVGSVGElement>(null);

  useEffect(() => {
    if (!svgRef.current || graph.nodes.length === 0) return;

    const layout = layoutDag(graph);
    const svg = d3.select(svgRef.current);
    svg.selectAll('*').remove();

    svg.attr('width', layout.width).attr('height', layout.height);

    const defs = svg.append('defs');
    defs
      .append('marker')
      .attr('id', 'arrowhead')
      .attr('viewBox', '0 0 10 10')
      .attr('refX', 8)
      .attr('refY', 5)
      .attr('markerWidth', 6)
      .attr('markerHeight', 6)
      .attr('orient', 'auto')
      .append('path')
      .attr('d', 'M 0 0 L 10 5 L 0 10 Z')
      .attr('fill', '#8c8c8c');

    const line = d3.line<{ x: number; y: number }>()
      .x((d) => d.x)
      .y((d) => d.y)
      .curve(d3.curveBasis);

    layout.edges.forEach((edge: LayoutEdge) => {
      svg
        .append('path')
        .attr('d', line(edge.points)!)
        .attr('stroke', '#8c8c8c')
        .attr('stroke-width', 2)
        .attr('fill', 'none')
        .attr('marker-end', 'url(#arrowhead)');
    });

    layout.nodes.forEach((node: LayoutNode) => {
      const g = svg.append('g').attr('transform', `translate(${node.x - node.width / 2}, ${node.y - node.height / 2})`);

      const fillColor = STATUS_COLORS[node.status] || '#d9d9d9';
      const textColor = STATUS_TEXT_COLORS[node.status] || '#595959';

      g.append('rect')
        .attr('width', node.width)
        .attr('height', node.height)
        .attr('rx', 6)
        .attr('ry', 6)
        .attr('fill', fillColor)
        .attr('stroke', '#bfbfbf')
        .attr('stroke-width', 1);

      if (node.status === 'TASK_STATUS_RUNNING') {
        g.append('rect')
          .attr('width', node.width)
          .attr('height', node.height)
          .attr('rx', 6)
          .attr('ry', 6)
          .attr('fill', 'none')
          .attr('stroke', '#1677ff')
          .attr('stroke-width', 2)
          .attr('stroke-dasharray', '5,3')
          .append('animate')
          .attr('attributeName', 'stroke-dashoffset')
          .attr('from', '0')
          .attr('to', '16')
          .attr('dur', '1s')
          .attr('repeatCount', 'indefinite');
      }

      g.append('text')
        .attr('x', node.width / 2)
        .attr('y', 22)
        .attr('text-anchor', 'middle')
        .attr('font-size', '13px')
        .attr('font-weight', '600')
        .attr('fill', textColor)
        .text(node.label);

      g.append('text')
        .attr('x', node.width / 2)
        .attr('y', 42)
        .attr('text-anchor', 'middle')
        .attr('font-size', '11px')
        .attr('fill', textColor)
        .attr('opacity', 0.8)
        .text(node.handler);
    });
  }, [graph]);

  return <svg ref={svgRef} />;
};

export default DagVisualizer;
