import React, { useEffect, useState } from 'react';
import { Table, Tag, Progress } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { listWorkers } from '../../services/api';
import type { WorkerInfo } from '../../services/types';

const languageColors: Record<string, string> = {
  go: 'blue',
  python: 'gold',
  cpp: 'purple',
};

const healthColors: Record<string, string> = {
  healthy: 'green',
  unhealthy: 'orange',
  offline: 'red',
};

const WorkersPage: React.FC = () => {
  const [workers, setWorkers] = useState<WorkerInfo[]>([]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    setLoading(true);
    listWorkers({ pageSize: 50 })
      .then((data) => setWorkers(data.workers))
      .finally(() => setLoading(false));
  }, []);

  const columns: ColumnsType<WorkerInfo> = [
    { title: 'ID', dataIndex: 'id', key: 'id' },
    { title: 'Address', dataIndex: 'addr', key: 'addr' },
    {
      title: 'Language',
      key: 'language',
      render: (_, record) => {
        const lang = record.labels?.language || 'unknown';
        return <Tag color={languageColors[lang] || 'default'}>{lang.toUpperCase()}</Tag>;
      },
    },
    {
      title: 'Load',
      key: 'load',
      width: 200,
      render: (_, record) => {
        const percent = record.capacity > 0 ? Math.round((record.activeTasks / record.capacity) * 100) : 0;
        return (
          <div>
            <Progress
              percent={percent}
              size="small"
              status={percent >= 90 ? 'exception' : 'active'}
              format={() => `${record.activeTasks}/${record.capacity}`}
            />
          </div>
        );
      },
    },
    {
      title: 'Health',
      dataIndex: 'status',
      key: 'status',
      render: (status: string) => (
        <Tag color={healthColors[status] || 'default'}>{status.toUpperCase()}</Tag>
      ),
    },
    {
      title: 'Handlers',
      dataIndex: 'handlers',
      key: 'handlers',
      ellipsis: true,
      render: (handlers: string[]) => handlers?.join(', ') || '-',
    },
  ];

  return (
    <div>
      <Table
        dataSource={workers}
        columns={columns}
        rowKey="id"
        loading={loading}
        pagination={false}
        size="middle"
      />
    </div>
  );
};

export default WorkersPage;
