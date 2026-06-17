import axios from 'axios';
import { toast } from 'sonner';

const API_BASE = '/api/v1';
const TOKEN_KEY = 'sparklabx_token';
const USER_KEY = 'sparklabx_user';
const ROLE_KEY = 'sparklabx_role';

export interface User {
  id: string;
  username?: string;
  email?: string;
  name?: string;
  role: 'admin';
  admin_role?: 'admin' | 'superadmin';
}

function safeSetItem(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch (err) {
    console.warn(`localStorage.setItem(${key}) failed:`, err);
  }
}

function safeGetItem(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function safeRemoveItem(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    /* noop */
  }
}

// Auth state subscribers — components subscribe via authService.subscribe(cb)
// to re-render when the current user changes (login, logout, role refresh).
const authListeners = new Set<() => void>();
function notifyAuthChange() {
  authListeners.forEach((cb) => {
    try { cb(); } catch (e) { console.warn('authChange listener threw:', e); }
  });
}

class AuthService {
  private token: string | null = null;

  constructor() {
    this.token = safeGetItem(TOKEN_KEY);
    this.setupInterceptors();
  }

  private setUser(user: Record<string, unknown>) {
    safeSetItem(USER_KEY, JSON.stringify(user));
    notifyAuthChange();
  }

  subscribe(cb: () => void): () => void {
    authListeners.add(cb);
    return () => authListeners.delete(cb);
  }

  private setupInterceptors() {
    axios.interceptors.request.use((config) => {
      if (this.token) {
        config.headers.Authorization = `Bearer ${this.token}`;
      }
      return config;
    });

    axios.interceptors.response.use(
      (response) => response,
      (error) => {
        if (error.response?.status === 401) {
          const url = error.config?.url || '';
          const isAuthEndpoint = url.includes('/login') || url.includes('/auth') || url.includes('/admin/me');
          if (isAuthEndpoint) {
            this.clearAuth();
          }
        }
        if (error.response?.status === 403) {
          toast.error(error.response?.data?.error || 'Permission denied');
        }
        return Promise.reject(error);
      }
    );
  }

  async login(identifier: string, password: string): Promise<{ user: User }> {
    const response = await axios.post(`${API_BASE}/admin/login`, { username: identifier, password });
    const { token, admin } = response.data;
    this.token = token;
    const user: User = {
      id: admin.id, username: admin.username, email: admin.email,
      role: 'admin', admin_role: admin.role,
    };
    safeSetItem(TOKEN_KEY, token);
    safeSetItem(ROLE_KEY, 'admin');
    this.setUser(user as unknown as Record<string, unknown>);
    return { user };
  }

  async checkAuthStatus(): Promise<boolean> {
    if (!this.token) return false;
    try {
      const response = await axios.get(`${API_BASE}/admin/me`);
      this.setUser({ ...response.data, role: 'admin', admin_role: response.data.role || 'admin' });
      return true;
    } catch {
      this.clearAuth();
      return false;
    }
  }

  isAuthenticated(): boolean {
    return !!this.token;
  }

  getToken(): string | null {
    return this.token;
  }

  isAdmin(): boolean {
    // notebook-lite: every authenticated user is an admin.
    return this.isAuthenticated();
  }

  isSuperAdmin(): boolean {
    const user = this.getCurrentUser();
    return user?.admin_role === 'superadmin';
  }

  getCurrentUser(): User | null {
    const data = safeGetItem(USER_KEY);
    if (!data) return null;
    try { return JSON.parse(data); } catch { return null; }
  }

  logout() {
    this.clearAuth();
    window.location.href = '/';
  }

  async loginWithGoogle(accessToken: string): Promise<{ user: User }> {
    const response = await axios.post(`${API_BASE}/auth/google`, { access_token: accessToken });
    const { token, user } = response.data;
    this.token = token;
    safeSetItem(TOKEN_KEY, token);
    safeSetItem(ROLE_KEY, 'admin');
    this.setUser({ ...user, role: 'admin' });
    return { user: { ...user, role: 'admin' } };
  }

  async loginWithMicrosoft(accessToken: string): Promise<{ user: User }> {
    const response = await axios.post(`${API_BASE}/auth/microsoft`, { access_token: accessToken });
    const { token, user } = response.data;
    this.token = token;
    safeSetItem(TOKEN_KEY, token);
    safeSetItem(ROLE_KEY, 'admin');
    this.setUser({ ...user, role: 'admin' });
    return { user: { ...user, role: 'admin' } };
  }

  // Generic OIDC SSO: the backend completes the code flow and redirects back
  // with the app JWT in the URL fragment. Store it and hydrate the user.
  async loginWithOIDCToken(token: string): Promise<void> {
    this.token = token;
    safeSetItem(TOKEN_KEY, token);
    safeSetItem(ROLE_KEY, 'admin');
    await this.checkAuthStatus(); // populates the user via /admin/me
  }

  // Fields are optional: older backends or a stripped config may omit them, so
  // callers must optional-chain — the type reflects that reality.
  async getAuthConfig(): Promise<{
    oidc?: { enabled?: boolean; provider_name?: string };
  }> {
    const response = await axios.get(`${API_BASE}/auth/config`);
    return response.data;
  }

  // Full URL to kick off the backend OIDC flow. Goes through API_BASE so it works
  // wherever the app/API is mounted (not a hardcoded root path).
  oidcStartUrl(): string {
    return `${API_BASE}/auth/oidc/start`;
  }

  // Silently refresh the backend's stored OIDC passthrough tokens via a hidden
  // iframe (prompt=none). Resolves true if the IdP renewed without interaction,
  // false otherwise (IdP session ended, or third-party cookies blocked). Callers
  // treat false as "leave it" — the user re-logs in only when they actually need
  // to. Keeps the per-kernel Trino/SSO passthrough alive without any user action.
  silentRenewOIDC(timeoutMs = 12000): Promise<boolean> {
    return new Promise((resolve) => {
      if (typeof window === 'undefined' || typeof document === 'undefined') {
        resolve(false);
        return;
      }
      const iframe = document.createElement('iframe');
      iframe.style.display = 'none';
      let done = false;
      const finish = (result: boolean) => {
        if (done) return;
        done = true;
        window.removeEventListener('message', onMsg);
        clearTimeout(timer);
        iframe.remove();
        resolve(result);
      };
      const onMsg = (e: MessageEvent) => {
        if (e.origin !== window.location.origin) return;
        if (e.data && e.data.type === 'sparklabx-oidc') finish(!!e.data.ok);
      };
      const timer = window.setTimeout(() => finish(false), timeoutMs);
      window.addEventListener('message', onMsg);
      iframe.src = `${this.oidcStartUrl()}?silent=1`;
      document.body.appendChild(iframe);
    });
  }

  private clearAuth() {
    this.token = null;
    safeRemoveItem(TOKEN_KEY);
    safeRemoveItem(USER_KEY);
    safeRemoveItem(ROLE_KEY);
    notifyAuthChange();
  }
}

const authService = new AuthService();
export default authService;
