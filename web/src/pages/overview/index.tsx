import React, { useEffect, useState } from 'react';
import { Card, Col, Row, Table, Tag } from 'antd';
import { StatisticCard } from '@ant-design/pro-components';
import {
  DashboardOutlined,
  ClusterOutlined,
  CheckCircleOutlined,
  FieldTimeOutlined,
} from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { getOverview, listWorkflows } from '../../services/api';
import type { OverviewData, WorkflowInstance } from '../../services/types';

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

const OverviewPage: React.FC = () => {
  const [overview, setOverview] = useState<OverviewData | null>(null);
  const [recentWorkflows, setRecentWorkflows] = useState<WorkflowInstance[]>([]);
  const navigate = useNavigate();

  useEffect(() => {
    getOverview().then(setOverview);
    listWorkflows({ pageSize: 10 }).then((data) => setRecentWorkflows(data.workflows));
  }, []);

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', ellipsis: true },
    { title: 'Name', dataIndex: 'name', key: 'name' },
    {
      title: 'Status',
      dataIndex: 'status',
      key: 'status',
      render: (status: string) => (
        <Tag color={statusColorMap[status] || 'default'}>
          {statusLabelMap[status] || status}
        </Tag>
      ),
    },
    { title: 'Created', dataIndex: 'createdAt', key: 'createdAt' },
  ];

  return (
    <div>
      <Row gutter={[16, 16]} style={{ marginBottom: 24 }}>
        <Col span={6}>
          <StatisticCard
            statistic={{
              title: 'Active Workflows',
              value: overview?.activeWorkflows ?? '-',
              icon: <DashboardOutlined style={{ fontSize: 24, color: '#1890ff' }} />,
            }}
          />
        </Col>
        <Col span={6}>
          <StatisticCard
            statistic={{
              title: 'Workers',
              value: overview ? `${overview.healthyWorkers}/${overview.totalWorkers}` : '-',
              icon: <ClusterOutlined style={{ fontSize: 24, color: '#52c41a' }} />,
            }}
          />
        </Col>
        <Col span={6}>
          <StatisticCard
            statistic={{
              title: 'Success Rate',
              value: overview ? `${(overview.successRate * 100).toFixed(1)}%` : '-',
              icon: <CheckCircleOutlined style={{ fontSize: 24, color: '#faad14' }} />,
            }}
          />
        </Col>
        <Col span={6}>
          <StatisticCard
            statistic={{
              title: 'Queue Depth',
              value: overview?.queueDepth ?? '-',
              icon: <FieldTimeOutlined style={{ fontSize: 24, color: '#722ed1' }} />,
            }}
          />
        </Col>
      </Row>

      <Card title="Recent Workflows">
        <Table
          dataSource={recentWorkflows}
          columns={columns}
          rowKey="id"
          pagination={false}
          size="small"
          onRow={(record) => ({
            onClick: () => navigate(`/workflows/${record.id}`),
            style: { cursor: 'pointer' },
          })}
        />
      </Card>
    </div>
  );
};

export default OverviewPage;
