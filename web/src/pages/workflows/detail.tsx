import React, { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Card, Descriptions, Tag, Table, Button, Spin } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { getWorkflow } from '../../services/api';
import type { WorkflowInstance, TaskInstance } from '../../services/types';
import DagVisualizer from '../../components/dag-visualizer';
import type { DagGraph } from '../../components/dag-visualizer/types';

const statusColorMap: Record<string, string> = {
  WORKFLOW_STATUS_PENDING: 'default',
  WORKFLOW_STATUS_RUNNING: 'processing',
  WORKFLOW_STATUS_COMPLETED: 'success',
  WORKFLOW_STATUS_FAILED: 'error',
  WORKFLOW_STATUS_CANCELLED: 'warning',
  WORKFLOW_STATUS_COMPENSATING: 'orange',
};

const taskStatusColorMap: Record<string, string> = {
  TASK_STATUS_PENDING: 'default',
  TASK_STATUS_READY: 'default',
  TASK_STATUS_SCHEDULED: 'cyan',
  TASK_STATUS_RUNNING: 'processing',
  TASK_STATUS_COMPLETED: 'success',
  TASK_STATUS_FAILED: 'error',
  TASK_STATUS_SKIPPED: 'warning',
  TASK_STATUS_COMPENSATING: 'orange',
};

function buildDagGraph(tasks: TaskInstance[]): DagGraph {
  const nodes = tasks.map((t) => ({
    id: t.taskName,
    label: t.taskName,
    handler: t.handler,
    status: t.status,
  }));

  const edges: { source: string; target: string }[] = [];
  for (const task of tasks) {
    if (task.dependsOn) {
      for (const dep of task.dependsOn) {
        edges.push({ source: dep, target: task.taskName });
      }
    }
  }

  return { nodes, edges };
}

function formatDuration(start: string, end: string): string {
  if (!start) return '-';
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : Date.now();
  const diff = Math.floor((e - s) / 1000);
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ${diff % 60}s`;
  return `${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`;
}

const WorkflowDetailPage: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [workflow, setWorkflow] = useState<WorkflowInstance | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!id) return;
    setLoading(true);
    getWorkflow(id)
      .then(setWorkflow)
      .finally(() => setLoading(false));
  }, [id]);

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;
  if (!workflow) return <div>Workflow not found</div>;

  const dagGraph = buildDagGraph(workflow.tasks);

  const taskColumns = [
    { title: 'Task Name', dataIndex: 'taskName', key: 'taskName' },
    { title: 'Handler', dataIndex: 'handler', key: 'handler' },
    {
      title: 'Status',
      dataIndex: 'status',
      key: 'status',
      render: (status: string) => (
        <Tag color={taskStatusColorMap[status] || 'default'}>
          {status.replace('TASK_STATUS_', '')}
        </Tag>
      ),
    },
    { title: 'Worker', dataIndex: 'workerId', key: 'workerId' },
    {
      title: 'Attempt',
      key: 'attempt',
      render: (_: unknown, record: TaskInstance) => `${record.attempt}/${record.maxAttempts}`,
    },
    { title: 'Error', dataIndex: 'errorMsg', key: 'errorMsg', ellipsis: true },
  ];

  return (
    <div>
      <Button
        icon={<ArrowLeftOutlined />}
        onClick={() => navigate('/workflows')}
        style={{ marginBottom: 16 }}
      >
        Back
      </Button>

      <Card title="Workflow Info" style={{ marginBottom: 16 }}>
        <Descriptions column={3}>
          <Descriptions.Item label="ID">{workflow.id}</Descriptions.Item>
          <Descriptions.Item label="Name">{workflow.name}</Descriptions.Item>
          <Descriptions.Item label="Status">
            <Tag color={statusColorMap[workflow.status] || 'default'}>
              {workflow.status.replace('WORKFLOW_STATUS_', '')}
            </Tag>
          </Descriptions.Item>
          <Descriptions.Item label="Created">{workflow.createdAt}</Descriptions.Item>
          <Descriptions.Item label="Duration">
            {formatDuration(workflow.startedAt, workflow.finishedAt)}
          </Descriptions.Item>
          {workflow.errorMsg && (
            <Descriptions.Item label="Error">{workflow.errorMsg}</Descriptions.Item>
          )}
        </Descriptions>
      </Card>

      <Card title="DAG Visualization" style={{ marginBottom: 16 }}>
        <div style={{ overflow: 'auto', padding: 16 }}>
          <DagVisualizer graph={dagGraph} />
        </div>
      </Card>

      <Card title="Tasks">
        <Table
          dataSource={workflow.tasks}
          columns={taskColumns}
          rowKey="id"
          pagination={false}
          size="small"
        />
      </Card>
    </div>
  );
};

export default WorkflowDetailPage;
