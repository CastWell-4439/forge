export type WorkflowStatus =
  | 'WORKFLOW_STATUS_UNSPECIFIED'
  | 'WORKFLOW_STATUS_PENDING'
  | 'WORKFLOW_STATUS_RUNNING'
  | 'WORKFLOW_STATUS_COMPLETED'
  | 'WORKFLOW_STATUS_FAILED'
  | 'WORKFLOW_STATUS_CANCELLED'
  | 'WORKFLOW_STATUS_COMPENSATING';

export type TaskStatus =
  | 'TASK_STATUS_UNSPECIFIED'
  | 'TASK_STATUS_PENDING'
  | 'TASK_STATUS_READY'
  | 'TASK_STATUS_SCHEDULED'
  | 'TASK_STATUS_RUNNING'
  | 'TASK_STATUS_COMPLETED'
  | 'TASK_STATUS_FAILED'
  | 'TASK_STATUS_SKIPPED'
  | 'TASK_STATUS_COMPENSATING';

export interface OverviewData {
  activeWorkflows: number;
  totalWorkflows: number;
  totalWorkers: number;
  healthyWorkers: number;
  successRate: number;
  queueDepth: number;
  failedWorkflows: number;
}

export interface TaskInstance {
  id: string;
  taskName: string;
  handler: string;
  status: TaskStatus;
  workerId: string;
  input: string;
  output: string;
  errorMsg: string;
  attempt: number;
  maxAttempts: number;
  scheduledAt: string;
  startedAt: string;
  finishedAt: string;
  createdAt: string;
  dependsOn?: string[];
}

export interface WorkflowInstance {
  id: string;
  name: string;
  version: number;
  status: WorkflowStatus;
  input: string;
  output: string;
  errorMsg: string;
  tasks: TaskInstance[];
  startedAt: string;
  finishedAt: string;
  createdAt: string;
  dagYaml?: string;
}

export interface WorkflowListData {
  workflows: WorkflowInstance[];
  nextPageToken: string;
}

export interface WorkerInfo {
  id: string;
  addr: string;
  labels: Record<string, string>;
  capacity: number;
  activeTasks: number;
  status: 'healthy' | 'unhealthy' | 'offline';
  handlers: string[];
}

export interface WorkerListData {
  workers: WorkerInfo[];
  nextPageToken: string;
}

export interface ListWorkflowsParams {
  status?: number;
  pageSize?: number;
  pageToken?: string;
}

export interface ListWorkersParams {
  languageFilter?: string;
  pageSize?: number;
  pageToken?: string;
}
