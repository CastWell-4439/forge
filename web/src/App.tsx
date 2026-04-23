import React from 'react';
import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom';
import { ProLayout } from '@ant-design/pro-components';
import {
  DashboardOutlined,
  ApartmentOutlined,
  ClusterOutlined,
} from '@ant-design/icons';
import OverviewPage from './pages/overview';
import WorkflowListPage from './pages/workflows/list';
import WorkflowDetailPage from './pages/workflows/detail';
import WorkersPage from './pages/workers';

const menuRoutes = {
  routes: [
    {
      path: '/overview',
      name: 'Overview',
      icon: <DashboardOutlined />,
    },
    {
      path: '/workflows',
      name: 'Workflows',
      icon: <ApartmentOutlined />,
    },
    {
      path: '/workers',
      name: 'Workers',
      icon: <ClusterOutlined />,
    },
  ],
};

const LayoutWrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const navigate = useNavigate();
  const location = useLocation();

  return (
    <ProLayout
      title="Forge"
      logo={null}
      route={menuRoutes}
      location={{ pathname: location.pathname }}
      menuItemRender={(item, dom) => (
        <div onClick={() => item.path && navigate(item.path)}>{dom}</div>
      )}
      fixSiderbar
      layout="mix"
      contentStyle={{ padding: 24 }}
    >
      {children}
    </ProLayout>
  );
};

const App: React.FC = () => {
  return (
    <BrowserRouter>
      <LayoutWrapper>
        <Routes>
          <Route path="/overview" element={<OverviewPage />} />
          <Route path="/workflows" element={<WorkflowListPage />} />
          <Route path="/workflows/:id" element={<WorkflowDetailPage />} />
          <Route path="/workers" element={<WorkersPage />} />
          <Route path="*" element={<Navigate to="/overview" replace />} />
        </Routes>
      </LayoutWrapper>
    </BrowserRouter>
  );
};

export default App;
