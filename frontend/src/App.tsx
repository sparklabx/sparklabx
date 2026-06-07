import React, { useState, useEffect } from 'react';
import { BrowserRouter as Router, Routes, Route, Navigate, useLocation } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ThemeProvider } from './components/theme-provider';
import { Toaster } from './components/ui/toaster';
import { Toaster as SonnerToaster } from './components/ui/sonner';
import { Sidebar } from './components/layout/Sidebar';
import { Header } from './components/layout/Header';
import AdminLogin from './components/Admin/AdminLogin';
import authService from './services/authService';
import './App.css';

// Lazy-loaded pages (notebook-lite scope: just notebooks + storage + admin)
const SettingsLayout = React.lazy(() => import('./components/Admin/Settings/SettingsLayout'));
const StoragePage = React.lazy(() => import('./components/Admin/StoragePage'));
const UserManagement = React.lazy(() => import('./components/Admin/UserManagement'));
const NotebookListPage = React.lazy(() => import('./components/Notebooks/NotebookListPage'));
const NotebookPage = React.lazy(() => import('./components/Notebooks/NotebookPage'));

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30000,
      gcTime: 5 * 60 * 1000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

const AdminLayout: React.FC = () => {
  const [collapsed, setCollapsed] = useState(false);
  const location = useLocation();
  const isNotebook = location.pathname.startsWith('/notebooks/') && location.pathname !== '/notebooks';

  return (
    <div className="h-screen bg-background overflow-hidden">
      <Sidebar collapsed={collapsed} onCollapse={setCollapsed} />
      <div className={`${collapsed ? "ml-16" : "ml-64"} h-full flex transition-[margin-left] duration-300 overflow-hidden`}>
        <div className="flex-1 flex flex-col min-w-0 h-full overflow-hidden">
          <Header />
          <main className={`flex-1 min-h-0 overflow-auto ${isNotebook ? 'p-0' : 'p-6'}`}>
            <React.Suspense fallback={<div className="p-10">Loading...</div>}>
              <Routes>
                <Route path="/" element={<Navigate to="/notebooks" replace />} />
                <Route path="/admin/storage" element={<StoragePage />} />
                <Route path="/admin/settings/*" element={<SettingsLayout />} />
                <Route path="/admin/users" element={<UserManagement />} />
                <Route path="/notebooks" element={<NotebookListPage />} />
                <Route path="/notebooks/:id" element={<NotebookPage />} />
                <Route path="*" element={<Navigate to="/notebooks" replace />} />
              </Routes>
            </React.Suspense>
          </main>
          {!isNotebook && (
            <footer className="shrink-0 border-t border-border bg-card px-6 py-3 text-center text-xs text-muted-foreground">
              &copy; {new Date().getFullYear()} SparkLabX
            </footer>
          )}
        </div>
      </div>
    </div>
  );
};

const App: React.FC = () => {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const checkAuth = async () => {
      try {
        const authenticated = await authService.checkAuthStatus();
        setIsAuthenticated(authenticated);
      } catch {
        setIsAuthenticated(false);
      } finally {
        setIsLoading(false);
      }
    };
    checkAuth();
  }, []);

  if (isLoading) {
    return <div className="flex h-screen items-center justify-center">Loading SparkLabX...</div>;
  }

  if (!isAuthenticated) {
    return (
      <ThemeProvider defaultTheme="system" storageKey="sparklabx-theme">
        <AdminLogin onSuccess={() => setIsAuthenticated(true)} />
      </ThemeProvider>
    );
  }

  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider defaultTheme="system" storageKey="sparklabx-theme">
        <Router>
          <AdminLayout />
          <Toaster />
          <SonnerToaster />
        </Router>
      </ThemeProvider>
    </QueryClientProvider>
  );
};

export default App;
