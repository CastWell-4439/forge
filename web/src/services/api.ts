import type {
  OverviewData,
  WorkflowInstance,
  WorkflowListData,
  WorkerListData,
  ListWorkflowsParams,
  ListWorkersParams,
} from './types';
import {
  getMockOverview,
  getMockWorkflows,
  getMockWorkflow,
  getMockWorkers,
} from './mock';

const BASE_URL = import.meta.env.VITE_API_BASE || 'http://localhost:8081';
const USE_MOCK = import.meta.env.VITE_USE_MOCK === 'true';

async function fetchJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url);
  if (!resp.ok) {
    throw new Error(`API error: ${resp.status} ${resp.statusText}`);
  }
  return resp.json() as Promise<T>;
}

export async function getOverview(): Promise<OverviewData> {
  if (USE_MOCK) return getMockOverview();
  return fetchJSON<OverviewData>(`${BASE_URL}/api/v1/overview`);
}

export async function listWorkflows(params?: ListWorkflowsParams): Promise<WorkflowListData> {
  if (USE_MOCK) return getMockWorkflows();
  const query = new URLSearchParams();
  if (params?.status) query.set('status', String(params.status));
  if (params?.pageSize) query.set('page_size', String(params.pageSize));
  if (params?.pageToken) query.set('page_token', params.pageToken);
  const qs = query.toString();
  return fetchJSON<WorkflowListData>(`${BASE_URL}/api/v1/workflows${qs ? '?' + qs : ''}`);
}

export async function getWorkflow(id: string): Promise<WorkflowInstance> {
  if (USE_MOCK) {
    const wf = getMockWorkflow(id);
    if (!wf) throw new Error(`Workflow ${id} not found`);
    return wf;
  }
  const data = await fetchJSON<{ workflow: WorkflowInstance }>(`${BASE_URL}/api/v1/workflows/${id}`);
  return data.workflow;
}

export async function cancelWorkflow(id: string, reason?: string): Promise<void> {
  if (USE_MOCK) return;
  await fetch(`${BASE_URL}/api/v1/workflows/${id}/cancel`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason: reason || '' }),
  });
}

export async function listWorkers(params?: ListWorkersParams): Promise<WorkerListData> {
  if (USE_MOCK) return getMockWorkers();
  const query = new URLSearchParams();
  if (params?.languageFilter) query.set('language_filter', params.languageFilter);
  if (params?.pageSize) query.set('page_size', String(params.pageSize));
  if (params?.pageToken) query.set('page_token', params.pageToken);
  const qs = query.toString();
  return fetchJSON<WorkerListData>(`${BASE_URL}/api/v1/workers${qs ? '?' + qs : ''}`);
}
