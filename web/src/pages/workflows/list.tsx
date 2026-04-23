import React, { useEffect, useState, useCallback } from 'react';
import { Tag, Select } from 'antd';
import { ProTable } from '@ant-design/pro-components';
import type { ProColumns } from '@ant-design/pro-components';
import { useNavigate } from 'react-router-dom';
import { listWorkflows } from '../../services/api';
import type { WorkflowInstance } from '../../services/types';

const statusOptions = [
  { label: 'All', value: 0 },
  { label: 'Pending', value: 1 },
  { label: 'Running', value: 2 },
  { label: 'Completed', value: 3 },
  { label: 'Failed', value: 4 },
  { label: 'Cancelled', value: 5 },
];

const statusColorMap: Record<string, string> = {
  WORKFLOW_STATUS_PENDING: 'default',
  WORKFLOW_STATUS_RUNNING: 'processing',
  WORKFLOW_STATUS_COMPLETED: 'success',
  WORKFLOW_STATUS_FAILED: 'error',
  WORKFLOW_STATUS_CANCELLED: 'warning',
  WORKFLOW_STATUS_COMPENSATING: 'orange',
};

const statusLabelMap: Record<string, string> = {
  WORKFLOW_STATUS_PENDING: 'Pending',
  WORKFLOW_STATUS_RUNNING: 'Running',
  WORKFLOW_STATUS_COMPLETED: 'Completed',
  WORKFLOW_STATUS_FAILED: 'Failed',
  WORKFLOW_STATUS_CANCELLED: 'Cancelled',
  WORKFLOW_STATUS_COMPENSATING: 'Compensating',
};

function formatDuration(start: string, end: string): string {
  if (!start) return '-';
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : Date.now();
  const diff = Math.floor((e - s) / 1000);
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ${diff % 60}s`;
  return `${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`;
}

const WorkflowListPage: React.FC = () => {
  const [workflows, setWorkflows] = useState<WorkflowInstance[]>([]);
  const [statusFilter, setStatusFilter] = useState<number>(0);
  const [pageToken, setPageToken] = useState<string>('');
  const [nextPageToken, setNextPageToken] = useState<string>('');
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();

  const fetchData = useCallback(async (status: number, token: string) => {
    setLoading(true);
    try {
      const data = await listWorkflows({
        status: status || undefined,
        pageSize: 20,
        pageToken: token || undefined,
      });
      setWorkflows(data.workflows);
      setNextPageToken(data.nextPageToken);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData(statusFilter, pageToken);
  }, [statusFilter, pageToken, fetchData]);

  const columns: ProColumns<WorkflowInstance>[] = [
    { title: 'ID', dataIndex: 'id', key: 'id', ellipsis: true, width: 280 },
    { title: 'Name', dataIndex: 'name', key: 'name', width: 200 },
    {
      title: 'Status',
      dataIndex: 'status',
      key: 'status',
      width: 140,
      render: (_, record) => (
        <Tag color={statusColorMap[record.status] || 'default'}>
          {statusLabelMap[record.status] || record.status}
        </Tag>
      ),
    },
    { title: 'Created', dataIndex: 'createdAt', key: 'createdAt', width: 200 },
    {
      title: 'Duration',
      key: 'duration',
      width: 120,
      render: (_, record) => formatDuration(record.startedAt, record.finishedAt),
    },
  ];

  return (
    <div>
      <div style={{ marginBottom: 16 }}>
        <span style={{ marginRight: 8 }}>Status:</span>
        <Select
          style={{ width: 160 }}
          value={statusFilter}
          onChange={(val) => {
            setStatusFilter(val);
            setPageToken('');
          }}
          options={statusOptions}
        />
      </div>
      <ProTable<WorkflowInstance>
        columns={columns}
        dataSource={workflows}
        rowKey="id"
        loading={loading}
        search={false}
        options={false}
        pagination={{
          pageSize: 20,
          showSizeChanger: false,
          onChange: () => {
            if (nextPageToken) setPageToken(nextPageToken);
          },
        }}
        onRow={(record) => ({
          onClick: () => navigate(`/workflows/${record.id}`),
          style: { cursor: 'pointer' },
        })}
      />
    </div>
  );
};

export default WorkflowListPage;
