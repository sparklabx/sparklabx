import { useNavigate, useLocation } from 'react-router-dom';
import { cn } from '@/lib/utils';
import {
  Notebook as NotebookIcon,
  ChevronLeft,
  ChevronRight,
  LogOut,
  Settings,
  Users,
  Database,
} from 'lucide-react';
import authService from '@/services/authService';
import { useCurrentUser } from '@/hooks/useCurrentUser';
import { useTheme } from '@/components/theme-provider';

interface SidebarProps {
  collapsed: boolean;
  onCollapse: (collapsed: boolean) => void;
}

export function Sidebar({ collapsed, onCollapse }: SidebarProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const { user: currentUser } = useCurrentUser();
  const { theme } = useTheme();
  const isDark = theme === 'dark' || (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches);

  const handleLogout = async () => {
    await authService.logout();
  };

  const isActive = (path: string) => {
    return location.pathname === path || location.pathname.startsWith(path);
  };

  return (
    <div
      className={cn(
        "fixed left-0 top-0 bottom-0 z-50 flex flex-col bg-card border-r border-border transition-all duration-300",
        collapsed ? "w-16" : "w-64"
      )}
    >
      {/* Logo */}
      <div className={cn(
        "flex items-center border-b border-border transition-all duration-300",
        collapsed ? "h-16 justify-center px-2" : "h-16 px-4"
      )}>
        <img
          src={collapsed
            ? (isDark ? "/logo-icon-dark.png?v=2" : "/logo-icon.png")
            : (isDark ? "/logo-dark.png?v=2" : "/logo.png")
          }
          alt="SparkLabX"
          className={cn(collapsed ? "h-10" : "h-12")}
        />
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto py-4 px-2">
        <div className="space-y-1">
          <button
            onClick={() => navigate('/notebooks')}
            className={cn(
              "w-full flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors",
              "hover:bg-accent hover:text-accent-foreground",
              isActive('/notebooks') && "bg-primary text-primary-foreground",
              collapsed && "justify-center"
            )}
          >
            <NotebookIcon className="size-4" />
            {!collapsed && <span className="flex-1 text-left">Notebooks</span>}
          </button>

          <button
            onClick={() => navigate('/admin/storage')}
            className={cn(
              "w-full flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors",
              "hover:bg-accent hover:text-accent-foreground",
              isActive('/admin/storage') && "bg-primary text-primary-foreground",
              collapsed && "justify-center"
            )}
          >
            <Database className="size-4" />
            {!collapsed && <span className="flex-1 text-left">Storage</span>}
          </button>

          <button
            onClick={() => navigate('/admin/users')}
            className={cn(
              "w-full flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors",
              "hover:bg-accent hover:text-accent-foreground",
              isActive('/admin/users') && "bg-primary text-primary-foreground",
              collapsed && "justify-center"
            )}
          >
            <Users className="size-4" />
            {!collapsed && <span className="flex-1 text-left">Users</span>}
          </button>
        </div>
      </nav>

      {/* Bottom Section */}
      <div className="mt-auto px-2 pb-2 space-y-1">
        {/* Settings */}
        <button
          onClick={() => navigate('/admin/settings')}
          className={cn(
            "w-full flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors",
            "hover:bg-accent hover:text-accent-foreground text-muted-foreground",
            isActive('/admin/settings') && "bg-primary text-primary-foreground",
            collapsed && "justify-center"
          )}
        >
          <Settings className="size-4" />
          {!collapsed && <span>Settings</span>}
        </button>

        {/* User Profile */}
        <div className={cn(
          "flex items-center gap-3 px-3 py-2 rounded-md",
          collapsed && "justify-center"
        )}>
          <button
            onClick={collapsed ? handleLogout : undefined}
            className="flex-shrink-0"
            title={collapsed ? "Logout" : undefined}
          >
            <div className="size-8 rounded-full bg-primary flex items-center justify-center text-xs font-bold text-primary-foreground">
              {(currentUser?.username || currentUser?.name || currentUser?.email || 'U').charAt(0).toUpperCase()}
            </div>
          </button>

          {!collapsed && (
            <>
              <div className="flex-1 min-w-0">
                <div className="text-sm font-medium truncate flex items-center gap-1.5">
                  {/* OAuth users don't have a username; show whichever identifier exists. */}
                  {currentUser?.username || currentUser?.name || currentUser?.email || 'User'}
                  {(currentUser as any)?.admin_role === 'superadmin' && (
                    <span className="text-[9px] px-1 py-0.5 rounded bg-amber-500/20 text-amber-600 font-semibold uppercase">super</span>
                  )}
                </div>
                <div className="text-xs text-muted-foreground truncate">
                  {currentUser?.email || ''}
                </div>
              </div>
              <button
                onClick={handleLogout}
                className="p-1.5 rounded-md hover:bg-accent transition-colors"
                title="Logout"
              >
                <LogOut className="size-4" />
              </button>
            </>
          )}
        </div>

        {/* Collapse Toggle */}
        <button
          onClick={() => onCollapse(!collapsed)}
          className={cn(
            "w-full flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors",
            "hover:bg-accent hover:text-accent-foreground text-muted-foreground",
            collapsed && "justify-center"
          )}
        >
          {collapsed ? <ChevronRight className="size-4" /> : <ChevronLeft className="size-4" />}
          {!collapsed && <span>Collapse</span>}
        </button>
      </div>
    </div>
  );
}

