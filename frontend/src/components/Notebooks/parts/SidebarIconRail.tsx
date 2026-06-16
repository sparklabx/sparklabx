import React from 'react';
import { Folder, HardDrive, List, Database } from 'lucide-react';

export type SidebarTab = 'workspace' | 'catalog' | 'files' | 'toc' | 'settings';

interface SidebarIconRailProps {
    sidebarTab: SidebarTab;
    sidebarOpen: boolean;
    onPick: (tab: SidebarTab) => void;
    // The "Data Sources" tab is always available — even with no connectors yet a
    // superadmin can open the panel to add one. (Kept as a flag for future gating.)
    showDataSources?: boolean;
}

type TabDef = { key: SidebarTab; icon: React.ElementType; title: string; iconClassName?: string };

export const SidebarIconRail: React.FC<SidebarIconRailProps> = ({ sidebarTab, sidebarOpen, onPick, showDataSources = true }) => {
    const tabs: TabDef[] = [
        { key: 'workspace', icon: Folder, title: 'Notebooks' },
        { key: 'files', icon: HardDrive, title: 'My Files' },
        // Generic "Data Sources" — connectors (Trino, Postgres, …) live inside the
        // panel, each with its own icon. Sits before the table of contents.
        ...(showDataSources ? [{ key: 'catalog' as SidebarTab, icon: Database, title: 'Data Sources' }] : []),
        { key: 'toc', icon: List, title: 'Table of Contents' },
    ];
    return (
        <aside className="w-12 flex flex-col items-center py-2 border-r border-border bg-muted/50 shrink-0">
            {tabs.map(({ key, icon: Icon, title, iconClassName }) => (
                <div
                    key={key}
                    className={`mb-4 cursor-pointer p-2 rounded transition-colors ${sidebarTab === key && sidebarOpen ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-muted hover:text-foreground'}`}
                    onClick={() => onPick(key)}
                    title={title}
                >
                    <Icon className={iconClassName ?? 'size-5'} />
                </div>
            ))}
        </aside>
    );
};
