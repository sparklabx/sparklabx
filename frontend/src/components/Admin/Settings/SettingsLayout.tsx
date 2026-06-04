import React from 'react';
import { NavLink, Routes, Route, Navigate } from 'react-router-dom';
import { Shield, Settings as SettingsIcon } from 'lucide-react';
import { cn } from '@/lib/utils';
import AllowedDomainsSection from './AllowedDomainsSection';

interface NavItem {
  to: string;
  label: string;
  icon: React.ElementType;
}

const NAV_ITEMS: NavItem[] = [
  { to: 'allowed-domains', label: 'Allowed Domains', icon: Shield },
];

const SettingsLayout: React.FC = () => {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold flex items-center gap-2">
          <SettingsIcon className="h-6 w-6" /> Settings
        </h1>
        <p className="text-muted-foreground">Manage system configuration</p>
      </div>

      <div className="grid grid-cols-[220px_1fr] gap-6">
        <nav className="space-y-1">
          {NAV_ITEMS.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2 px-3 py-2 rounded-md text-sm transition-colors',
                  isActive
                    ? 'bg-primary text-primary-foreground'
                    : 'hover:bg-muted text-muted-foreground hover:text-foreground',
                )
              }
            >
              <Icon className="h-4 w-4" />
              {label}
            </NavLink>
          ))}
        </nav>

        <div className="min-w-0">
          <Routes>
            <Route index element={<Navigate to="allowed-domains" replace />} />
            <Route path="allowed-domains" element={<AllowedDomainsSection />} />
            <Route path="*" element={<Navigate to="allowed-domains" replace />} />
          </Routes>
        </div>
      </div>
    </div>
  );
};

export default SettingsLayout;
