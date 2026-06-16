import React from 'react';
import { Folder, HardDrive, List, Database } from 'lucide-react';
import { TrinoIcon } from './TrinoIcon';

export type SidebarTab = 'workspace' | 'catalog' | 'files' | 'toc' | 'settings';

interface SidebarIconRailProps {
    sidebarTab: SidebarTab;
    sidebarOpen: boolean;
    onPick: (tab: SidebarTab) => void;
    // Show the catalog tab only when at least one connector is configured. The
    // kinds drive the tab glyph: a lone Trino keeps its on-brand mark; mixed or
    // other connectors fall back to a generic database icon.
    connectorKinds?: string[];
}

// iconClassName lets a non-square glyph keep its aspect (the Trino mark is
// portrait); lucide icons stay the default square size-5.
type TabDef = { key: SidebarTab; icon: React.ElementType; title: string; iconClassName?: string };

export const SidebarIconRail: React.FC<SidebarIconRailProps> = ({ sidebarTab, sidebarOpen, onPick, connectorKinds }) => {
    const kinds = connectorKinds ?? [];
    const onlyTrino = kinds.length === 1 && kinds[0] === 'trino';
    // The real Trino rabbit mark (monochrome currentColor, face as cut-out holes)
    // when Trino is the sole connector; a generic database glyph otherwise.
    const catalogTab: TabDef = onlyTrino
        ? { key: 'catalog', icon: TrinoIcon, title: 'Trino Catalog', iconClassName: 'h-5 w-auto' }
        : { key: 'catalog', icon: Database, title: 'Data Catalog' };
    const tabs: TabDef[] = [
        { key: 'workspace', icon: Folder, title: 'Notebooks' },
        { key: 'files', icon: HardDrive, title: 'My Files' },
        // Catalog browser sits at position 3 (before the table of contents),
        // shown only when a connector is configured.
        ...(kinds.length > 0 ? [catalogTab] : []),
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
