import axios from 'axios';

const API_BASE = '/api/v1/allowed-domains';

export type RuleType = 'domain' | 'exact_email';

export interface AllowedRule {
  id: string;
  rule_type: RuleType;
  value: string;
  enabled: boolean;
  added_by: string;
  note: string;
  created_at: string;
}

export interface CreateRuleRequest {
  rule_type: RuleType;
  value: string;
  note?: string;
}

export interface UpdateRuleRequest {
  enabled?: boolean;
  note?: string;
}

const allowedDomainService = {
  async list(): Promise<AllowedRule[]> {
    const { data } = await axios.get<{ rules: AllowedRule[] }>(API_BASE);
    return data.rules ?? [];
  },

  async create(payload: CreateRuleRequest, force = false): Promise<AllowedRule> {
    const url = force ? `${API_BASE}?force=true` : API_BASE;
    const { data } = await axios.post<AllowedRule>(url, payload);
    return data;
  },

  async update(id: string, payload: UpdateRuleRequest): Promise<void> {
    await axios.patch(`${API_BASE}/${id}`, payload);
  },

  async delete(id: string): Promise<void> {
    await axios.delete(`${API_BASE}/${id}`);
  },
};

export default allowedDomainService;
