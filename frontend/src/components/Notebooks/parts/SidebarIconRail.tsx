import React from 'react';
import { Folder, HardDrive, List } from 'lucide-react';
import { TrinoIcon } from './TrinoIcon';

export type SidebarTab = 'workspace' | 'catalog' | 'files' | 'toc' | 'settings';

interface SidebarIconRailProps {
    sidebarTab: SidebarTab;
    sidebarOpen: boolean;
    onPick: (tab: SidebarTab) => void;
    trinoEnabled?: boolean; // show the Trino catalog tab only when configured
}

// iconClassName lets a non-square glyph keep its aspect (the Trino mark is
// portrait); lucide icons stay the default square size-5.
type TabDef = { key: SidebarTab; icon: React.ElementType; title: string; iconClassName?: string };

export const SidebarIconRail: React.FC<SidebarIconRailProps> = ({ sidebarTab, sidebarOpen, onPick, trinoEnabled }) => {
    const tabs: TabDef[] = [
        { key: 'workspace', icon: Folder, title: 'Notebooks' },
        { key: 'files', icon: HardDrive, title: 'My Files' },
        // Trino catalog sits at position 3 (before the table of contents). The real
        // Trino rabbit mark, monochrome (currentColor) with the face as cut-out
        // holes — on-brand AND consistent/color-reactive like the icons around it.
        ...(trinoEnabled ? [{ key: 'catalog' as SidebarTab, icon: TrinoIcon, title: 'Trino Catalog', iconClassName: 'h-5 w-auto' }] : []),
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
