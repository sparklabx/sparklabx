import React, { useState } from 'react';
import { Gauge } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import authService from '@/services/authService';

// "Spark UI" toolbar button → opens the kernel's Spark Web UI (DAGs, stages, the
// SQL tab with execution plans, metrics) in an in-app iframe. The backend proxy
// authenticates via the ?token= query param (an iframe can't send an auth
// header), then sets a path-scoped cookie so the UI's own asset/XHR requests
// work too. The iframe only mounts while open, so we don't proxy when unused.
export const SparkUIDialog: React.FC<{ notebookId: string; disabled?: boolean; compact?: boolean }> = ({ notebookId, disabled }) => {
    const [open, setOpen] = useState(false);
    const token = authService.getToken() || '';
    const src = `/api/v1/notebooks/${notebookId}/kernel/spark-ui/?token=${encodeURIComponent(token)}`;

    return (
        <>
            <Button
                variant="ghost"
                size="icon"
                disabled={disabled}
                onClick={() => setOpen(true)}
                className="h-8 w-8"
                title="Open the Spark UI (DAG, stages, SQL plans, metrics)"
            >
                <Gauge className="size-4 text-amber-500 animate-pulse" />
            </Button>
            <Dialog open={open} onOpenChange={setOpen}>
                <DialogContent className="max-w-[96vw] w-[96vw] h-[92vh] p-0 gap-0 flex flex-col">
                    <DialogHeader className="px-4 py-2 border-b shrink-0">
                        <DialogTitle className="text-sm">Spark UI</DialogTitle>
                    </DialogHeader>
                    {open && (
                        <iframe
                            src={src}
                            title="Spark UI"
                            className="w-full flex-1 border-0"
                        />
                    )}
                </DialogContent>
            </Dialog>
        </>
    );
};
