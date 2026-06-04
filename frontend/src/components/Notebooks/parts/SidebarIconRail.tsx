import React from 'react';
import { Folder, HardDrive, List } from 'lucide-react';

export type SidebarTab = 'workspace' | 'catalog' | 'files' | 'toc' | 'settings';

interface SidebarIconRailProps {
    sidebarTab: SidebarTab;
    sidebarOpen: boolean;
    onPick: (tab: SidebarTab) => void;
}

const tabs: { key: SidebarTab; icon: React.ElementType; title: string }[] = [
    { key: 'workspace', icon: Folder, title: 'Notebooks' },
    { key: 'files', icon: HardDrive, title: 'My Files' },
    { key: 'toc', icon: List, title: 'Table of Contents' },
];

export const SidebarIconRail: React.FC<SidebarIconRailProps> = ({ sidebarTab, sidebarOpen, onPick }) => (
    <aside className="w-12 flex flex-col items-center py-2 border-r border-border bg-muted/50 shrink-0">
        {tabs.map(({ key, icon: Icon, title }) => (
            <div
                key={key}
                className={`mb-4 cursor-pointer p-2 rounded transition-colors ${sidebarTab === key && sidebarOpen ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-muted hover:text-foreground'}`}
                onClick={() => onPick(key)}
                title={title}
            >
                <Icon className="size-5" />
            </div>
        ))}
    </aside>
);
