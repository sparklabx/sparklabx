import { useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Button } from '@/components/ui/button';
import {
    Download,
    Upload,
    Moon,
    Sun,
    Plus,
} from 'lucide-react';
import { useTheme } from '@/components/theme-provider';

interface MenuBarProps {
    onNewNotebook?: () => void;
    onExportHTML?: () => void;
    onImportNotebook?: (file: File) => void;
    isNotebookPage?: boolean;
}

export function MenuBar({
    onExportHTML,
    onImportNotebook,
    isNotebookPage = false,
}: MenuBarProps) {
    const navigate = useNavigate();
    const { theme, setTheme } = useTheme();
    const fileInputRef = useRef<HTMLInputElement>(null);

    const handleImportClick = () => {
        fileInputRef.current?.click();
    };

    const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
        const file = e.target.files?.[0];
        if (file && onImportNotebook) {
            onImportNotebook(file);
        }
        // Reset input
        e.target.value = '';
    };

    return (
        <div className="flex items-center gap-1">
            {/* Hidden file input for import */}
            <input
                ref={fileInputRef}
                type="file"
                accept=".json,.html"
                onChange={handleFileChange}
                className="hidden"
            />

            {/* File Menu */}
            <DropdownMenu>
                <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="sm" className="h-7 px-2 text-sm">
                        File
                    </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-48">
                    <DropdownMenuItem onClick={() => navigate('/notebooks')}>
                        <Plus className="mr-2 h-4 w-4" />
                        New Notebook
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onClick={handleImportClick}>
                        <Upload className="mr-2 h-4 w-4" />
                        Import Notebook...
                    </DropdownMenuItem>
                    {isNotebookPage && (
                        <DropdownMenuItem onClick={onExportHTML}>
                            <Download className="mr-2 h-4 w-4" />
                            Export as HTML
                        </DropdownMenuItem>
                    )}
                </DropdownMenuContent>
            </DropdownMenu>

            {/* View Menu */}
            <DropdownMenu>
                <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="sm" className="h-7 px-2 text-sm">
                        View
                    </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-48">
                    <DropdownMenuItem onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
                        {theme === 'dark' ? (
                            <Sun className="mr-2 h-4 w-4" />
                        ) : (
                            <Moon className="mr-2 h-4 w-4" />
                        )}
                        {theme === 'dark' ? 'Light Mode' : 'Dark Mode'}
                    </DropdownMenuItem>
                </DropdownMenuContent>
            </DropdownMenu>
        </div>
    );
}
