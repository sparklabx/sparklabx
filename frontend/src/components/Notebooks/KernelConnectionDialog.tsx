/**
 * KernelConnectionDialog
 * Unified dialog for connecting kernel
 */

import React, { useState, useEffect } from 'react';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion';
import { Loader2, AlertCircle, CheckCircle2, Server, Plus, X } from 'lucide-react';
import axios from 'axios';
import { fetchKernelSpecs, fetchResourcePresets, type KernelSpec, type ResourcePresetsResponse } from '@/services/notebookService';
import { toast } from 'sonner';

// ... (previous code)

interface KernelConnectionDialogProps {
    open: boolean;
    onClose: () => void;
    language: 'python' | 'scala';
    onConnect: (options: {
        enableSpark: boolean;
        kernelName?: string;
        sparkPackages?: string;
        icebergWarehousePath?: string;
        resourcePreset?: string;
        resourceCustom?: { cpu: string; memory: string };
    }) => Promise<void>;
    savedPackages?: string;
    savedIcebergWarehousePath?: string;
    savedResourcePreset?: string;
    savedResourceCustom?: { cpu: string; memory: string };
}

// "500m" → "0.5", "2" → "2" — humanize a k8s CPU quantity for display.
function formatCpu(cpu: string): string {
    if (cpu.endsWith('m')) {
        const milli = parseInt(cpu.slice(0, -1), 10);
        if (!Number.isNaN(milli)) return String(milli / 1000);
    }
    return cpu;
}

// "2Gi" → "2 GB", "512Mi" → "512 MB" — k8s memory quantity to a friendly unit.
function formatMem(mem: string): string {
    return mem.replace(/Gi?$/, ' GB').replace(/Mi?$/, ' MB');
}

// Inverse converters for the Custom inputs, which take plain numbers (cores /
// GB) instead of raw k8s quantities — typing "3" meaning 3 GB must never reach
// the backend as 3 bytes, and "3gb" must never be a parse error.
function coresFromQuantity(q: string): string {
    if (q.endsWith('m')) {
        const milli = parseInt(q.slice(0, -1), 10);
        if (!Number.isNaN(milli)) return String(milli / 1000);
    }
    return q;
}
function gbFromQuantity(q: string): string {
    if (q.endsWith('Gi')) return q.slice(0, -2);
    if (q.endsWith('Mi')) {
        const mi = parseFloat(q);
        if (!Number.isNaN(mi)) return String(mi / 1024);
    }
    return q;
}

interface PackagePreset {
    id: string;
    label: string;
    description: string;
    packages: string[];
}

const PACKAGE_PRESETS: PackagePreset[] = [
    {
        id: 'delta',
        label: 'Delta',
        description: 'Delta Lake for Spark 3.5 / Scala 2.12',
        packages: ['io.delta:delta-spark_2.12:3.3.2'],
    },
    {
        id: 'iceberg',
        label: 'Iceberg',
        description: 'Iceberg runtime for Spark 3.5 / Scala 2.12',
        packages: ['org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.10.1'],
    },
    {
        id: 'delta-iceberg',
        label: 'Delta + Iceberg',
        description: 'Enable both Delta Lake and Iceberg',
        packages: [
            'io.delta:delta-spark_2.12:3.3.2',
            'org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.10.1',
        ],
    },
];

function normalizePackageInput(value?: string): string {
    return (value || '')
        .split(/[,\n]/)
        .map(pkg => pkg.trim())
        .filter(Boolean)
        .join('\n');
}

function packageListFromInput(value?: string): string[] {
    return normalizePackageInput(value)
        .split('\n')
        .map(pkg => pkg.trim())
        .filter(Boolean);
}

export function KernelConnectionDialog({
    open,
    onClose,
    language,
    onConnect,
    savedPackages,
    savedIcebergWarehousePath,
    savedResourcePreset,
    savedResourceCustom,
}: KernelConnectionDialogProps) {
    // Kernel selection
    const [kernelSpecs, setKernelSpecs] = useState<Record<string, KernelSpec>>({});
    const [loadingKernelSpecs, setLoadingKernelSpecs] = useState(false);
    const [selectedKernelName, setSelectedKernelName] = useState<string>('');
    const [sparkPackages, setSparkPackages] = useState<string>('');
    const [activePresetId, setActivePresetId] = useState<string | null>(null);
    const [icebergWarehousePath, setIcebergWarehousePath] = useState<string>('');

    // Resource sizing (k8s_per_user, issue #41). resourceConfig.enabled gates the
    // whole section. selectedResource is a preset id, or the sentinel 'custom'.
    const [resourceConfig, setResourceConfig] = useState<ResourcePresetsResponse | null>(null);
    const [selectedResource, setSelectedResource] = useState<string>('');
    const [customCpu, setCustomCpu] = useState<string>('');
    const [customMemory, setCustomMemory] = useState<string>('');
    // Size of the user's kernel that is ALREADY running, if any. Shown as a
    // warning: the kernel is per-user, so picking a different size restarts it
    // for every notebook attached to it.
    const [runningSize, setRunningSize] = useState<{ cores: number; memGB: number } | null>(null);

    const [isSubmitting, setIsSubmitting] = useState(false);
    const packageRows = sparkPackages ? sparkPackages.split('\n') : [''];

    // Fetch kernel specs when dialog opens
    useEffect(() => {
        if (open && Object.keys(kernelSpecs).length === 0) {
            fetchKernelSpecsList();
        }
    }, [open]);

    // Fetch resource presets when dialog opens. Pick the saved preset, else the
    // configured default, else the first preset. 'custom' only if allowed.
    useEffect(() => {
        if (!open) return;
        let alive = true;
        // Best-effort: learn the size of the kernel that's already running so
        // the dialog can warn that picking a different one restarts it.
        (async () => {
            try {
                const res = await axios.get('/api/v1/kernel/usage', { timeout: 3000 });
                if (alive && res.data?.available) {
                    setRunningSize({
                        cores: res.data.cpu_limit_cores ?? 0,
                        memGB: (res.data.mem_limit_bytes ?? 0) / 1024 ** 3,
                    });
                } else if (alive) {
                    setRunningSize(null);
                }
            } catch {
                if (alive) setRunningSize(null);
            }
        })();
        (async () => {
            const cfg = await fetchResourcePresets();
            if (!alive) return;
            setResourceConfig(cfg);
            if (!cfg.enabled || cfg.presets.length === 0) return;
            const ids = cfg.presets.map(p => p.id);
            const initial =
                (savedResourcePreset && (ids.includes(savedResourcePreset) || (savedResourcePreset === 'custom' && cfg.allow_custom)) && savedResourcePreset) ||
                (cfg.default_preset && ids.includes(cfg.default_preset) && cfg.default_preset) ||
                ids[0];
            setSelectedResource(initial);
            if (savedResourceCustom) {
                // Saved values are k8s quantities ("1500m"/"3Gi") — the inputs
                // hold plain numbers.
                setCustomCpu(coresFromQuantity(savedResourceCustom.cpu));
                setCustomMemory(gbFromQuantity(savedResourceCustom.memory));
            }
        })();
        return () => { alive = false; };
    }, [open, savedResourcePreset, savedResourceCustom]);

    // Auto-select default kernel based on language
    useEffect(() => {
        if (Object.keys(kernelSpecs).length > 0 && !selectedKernelName) {
            console.log('[KernelDialog] Auto-selecting kernel for language:', language);
            // Default kernel selection based on language
            if (language === 'python' && kernelSpecs['pyspark']) {
                setSelectedKernelName('pyspark');
            } else if (language === 'scala' && kernelSpecs['scala212']) {
                setSelectedKernelName('scala212');
            } else {
                // Fallback to first available if specific defaults not found
                const first = Object.keys(kernelSpecs)[0];
                if (first) setSelectedKernelName(first);
            }
        }
    }, [kernelSpecs, language]);

    // Prefill saved packages when opening, reset kernel on close
    useEffect(() => {
        if (open) {
            setSparkPackages(normalizePackageInput(savedPackages));
            setIcebergWarehousePath(savedIcebergWarehousePath || '');
            const saved = normalizePackageInput(savedPackages)
                .split(/[,\n]/)
                .map(pkg => pkg.trim())
                .filter(Boolean);
            const exactPreset = PACKAGE_PRESETS.find((preset) =>
                preset.packages.length === saved.length &&
                preset.packages.every((pkg) => saved.includes(pkg))
            );
            setActivePresetId(exactPreset?.id || null);
        } else {
            setTimeout(() => {
                setSelectedKernelName('');
            }, 300);
        }
    }, [open, savedPackages, savedIcebergWarehousePath]);

    const fetchKernelSpecsList = async () => {
        setLoadingKernelSpecs(true);
        try {
            const response = await fetchKernelSpecs();
            // Response has nested kernelspecs: { kernelspecs: { kernelspecs: {...} } }
            const specs = (response.kernelspecs as { kernelspecs?: unknown })?.kernelspecs || response.kernelspecs || {};
            console.log('[KernelDialog] Fetched kernel specs:', specs);
            setKernelSpecs(specs as Record<string, KernelSpec>);
        } catch (error) {
            console.error('Failed to fetch kernel specs:', error);
            toast.error('Failed to load kernel options');
        } finally {
            setLoadingKernelSpecs(false);
        }
    };

    const applyPreset = (preset: PackagePreset) => {
        const current = packageListFromInput(sparkPackages);
        if (activePresetId === preset.id) {
            const remaining = current.filter(pkg => !preset.packages.includes(pkg));
            setSparkPackages(remaining.join('\n'));
            setActivePresetId(null);
            return;
        }

        const nextPreset = PACKAGE_PRESETS.find(item => item.id === preset.id);
        const withoutPreviousPreset = activePresetId
            ? current.filter(pkg => {
                const previousPreset = PACKAGE_PRESETS.find(item => item.id === activePresetId);
                return !previousPreset?.packages.includes(pkg);
            })
            : current;
        const merged = Array.from(new Set([...(withoutPreviousPreset || []), ...(nextPreset?.packages || [])]));
        setSparkPackages(merged.join('\n'));
        setActivePresetId(preset.id);
    };

    const updatePackageRow = (index: number, value: string) => {
        const nextRows = [...packageRows];
        nextRows[index] = value;
        setSparkPackages(normalizePackageInput(nextRows.join('\n')));
        setActivePresetId(null);
    };

    const addPackageRow = () => {
        setSparkPackages(packageRows.filter(Boolean).concat('').join('\n'));
        setActivePresetId(null);
    };

    const removePackageRow = (index: number) => {
        const nextRows = packageRows.filter((_, rowIndex) => rowIndex !== index);
        setSparkPackages(normalizePackageInput(nextRows.join('\n')));
        setActivePresetId(null);
    };

    const handleSubmit = async () => {
        setIsSubmitting(true);
        try {
            const packageList = packageListFromInput(sparkPackages);
            const normalizedPackages = packageList.join(',');
            const hasIcebergPackage = packageList.some(pkg => pkg.includes('org.apache.iceberg:iceberg-spark-runtime'));
            if (hasIcebergPackage && !icebergWarehousePath.trim()) {
                toast.error('Iceberg needs a warehouse path');
                setIsSubmitting(false);
                return;
            }

            // Resolve the chosen kernel-pod size, if the feature is enabled.
            let resourcePreset: string | undefined;
            let resourceCustom: { cpu: string; memory: string } | undefined;
            if (resourceConfig?.enabled) {
                if (selectedResource === 'custom') {
                    // Inputs are plain numbers: cores and GB. Convert to k8s
                    // quantities here ("1.5" → "1500m", "3" GB → "3Gi") so users
                    // never have to know quantity syntax.
                    const cpuN = parseFloat(customCpu);
                    const memN = parseFloat(customMemory);
                    if (!Number.isFinite(cpuN) || cpuN <= 0 || !Number.isFinite(memN) || memN <= 0) {
                        toast.error('Enter a valid CPU (cores) and memory (GB)');
                        setIsSubmitting(false);
                        return;
                    }
                    const maxCpuN = parseFloat(coresFromQuantity(resourceConfig.max_cpu));
                    if (Number.isFinite(maxCpuN) && maxCpuN > 0 && cpuN > maxCpuN) {
                        toast.error(`CPU exceeds the allowed max of ${maxCpuN}`);
                        setIsSubmitting(false);
                        return;
                    }
                    const maxMemN = parseFloat(gbFromQuantity(resourceConfig.max_memory));
                    if (Number.isFinite(maxMemN) && maxMemN > 0 && memN > maxMemN) {
                        toast.error(`Memory exceeds the allowed max of ${maxMemN} GB`);
                        setIsSubmitting(false);
                        return;
                    }
                    resourceCustom = {
                        cpu: Number.isInteger(cpuN) ? String(cpuN) : `${Math.round(cpuN * 1000)}m`,
                        memory: `${memN}Gi`,
                    };
                } else if (selectedResource) {
                    resourcePreset = selectedResource;
                }
            }

            await onConnect({
                enableSpark: false, // Explicitly false as per new requirement
                kernelName: selectedKernelName || undefined,
                sparkPackages: normalizedPackages || undefined,
                icebergWarehousePath: icebergWarehousePath.trim() || undefined,
                resourcePreset,
                resourceCustom,
            });
            onClose();
        } catch (error) {
            const e = error as { response?: { data?: { error?: string } }; message?: string };
            const errorMessage = e.response?.data?.error || e.message || 'Connection failed';
            toast.error(errorMessage);
        } finally {
            setIsSubmitting(false);
        }
    };

    return (
        <Dialog open={open} onOpenChange={(isOpen) => !isOpen && onClose()}>
            <DialogContent className="flex max-w-lg max-h-[85vh] flex-col overflow-hidden p-0">
                <DialogHeader>
                    <div className="px-6 pt-6">
                    <DialogTitle className="flex items-center gap-2">
                        <Server className="h-5 w-5 text-muted-foreground" />
                        Connect Kernel
                    </DialogTitle>
                    <DialogDescription>
                        {language === 'python' ? 'Python' : 'Scala'} notebook
                    </DialogDescription>
                    </div>
                </DialogHeader>

                <div className="flex-1 min-h-0 space-y-4 overflow-y-auto px-6 py-4">
                    {/* Kernel Selection */}
                    <div className="space-y-2">
                        <Label className="text-sm font-medium">Kernel</Label>
                        {loadingKernelSpecs ? (
                            <div className="flex items-center gap-2 p-3 border rounded-md">
                                <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                                <span className="text-sm text-muted-foreground">Loading kernels...</span>
                            </div>
                        ) : Object.keys(kernelSpecs).length === 0 ? (
                            <div className="p-3 border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-950 rounded-md">
                                <div className="flex items-start gap-2">
                                    <AlertCircle className="h-4 w-4 text-amber-600 dark:text-amber-400 mt-0.5" />
                                    <p className="text-sm text-amber-700 dark:text-amber-300">
                                        No kernels available. Please check server status.
                                    </p>
                                </div>
                            </div>
                        ) : (
                            <Select value={selectedKernelName} onValueChange={setSelectedKernelName}>
                                <SelectTrigger className="h-10">
                                    <SelectValue placeholder="Select a kernel..." />
                                </SelectTrigger>
                                <SelectContent>
                                    {(() => {
                                        const filtered = Object.entries(kernelSpecs)
                                            .filter(([, spec]) => {
                                                // Function to match language loosely
                                                const specLang = spec.language.toLowerCase();
                                                const targetLang = language.toLowerCase();

                                                if (targetLang === 'python') return specLang.includes('python');
                                                if (targetLang === 'scala') return specLang.includes('scala');
                                                return true;
                                            });

                                        return filtered.map(([name, spec]) => (
                                            <SelectItem key={name} value={name} className="text-sm">
                                                <div className="flex items-center gap-2">
                                                    <Server className="h-3.5 w-3.5 text-muted-foreground" />
                                                    <span>{spec.display_name}</span>
                                                </div>
                                            </SelectItem>
                                        ));
                                    })()}
                                </SelectContent>
                            </Select>
                        )}

                        {selectedKernelName && kernelSpecs[selectedKernelName] && (
                            <div className="p-2.5 bg-green-50 dark:bg-green-950 border border-green-200 dark:border-green-800 rounded-md">
                                <div className="flex items-start gap-2">
                                    <CheckCircle2 className="h-3.5 w-3.5 text-green-600 dark:text-green-400 mt-0.5" />
                                    <div className="flex-1">
                                        <div className="text-xs font-medium text-green-900 dark:text-green-100">
                                            {kernelSpecs[selectedKernelName].display_name}
                                        </div>
                                        <div className="text-xs text-green-700 dark:text-green-300 mt-0.5">
                                            Interactive {kernelSpecs[selectedKernelName].language} environment
                                        </div>
                                    </div>
                                </div>
                            </div>
                        )}
                    </div>

                    {/* Resources — kernel container/pod size (issue #41).
                        Hidden unless the admin configured presets. Vertical
                        radio list (the pattern Codespaces / Deepnote / JupyterHub
                        profile lists use): every option and its specs visible at
                        a glance, selected row highlighted, default badged. */}
                    {resourceConfig?.enabled && resourceConfig.presets.length > 0 && (
                        <div className="space-y-2">
                            <Label className="text-sm font-medium">Resources</Label>
                            <div className="divide-y divide-border overflow-hidden rounded-md border">
                                {resourceConfig.presets.map((preset) => {
                                    const active = selectedResource === preset.id;
                                    return (
                                        <button
                                            key={preset.id}
                                            type="button"
                                            onClick={() => setSelectedResource(preset.id)}
                                            className={`flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors ${
                                                active ? 'bg-primary/5' : 'hover:bg-muted/40'
                                            }`}
                                        >
                                            <span className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-full border ${
                                                active ? 'border-primary' : 'border-muted-foreground/40'
                                            }`}>
                                                {active && <span className="h-2 w-2 rounded-full bg-primary" />}
                                            </span>
                                            <span className="w-16 shrink-0 text-sm font-medium">{preset.label}</span>
                                            <span className="text-xs text-muted-foreground">
                                                {formatCpu(preset.cpu)} vCPU · {formatMem(preset.memory)} RAM
                                            </span>
                                            {preset.id === resourceConfig.default_preset && (
                                                <span className="ml-auto rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
                                                    Default
                                                </span>
                                            )}
                                        </button>
                                    );
                                })}
                                {resourceConfig.allow_custom && (
                                    <div className={selectedResource === 'custom' ? 'bg-primary/5' : ''}>
                                        <button
                                            type="button"
                                            onClick={() => setSelectedResource('custom')}
                                            className={`flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors ${
                                                selectedResource === 'custom' ? '' : 'hover:bg-muted/40'
                                            }`}
                                        >
                                            <span className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-full border ${
                                                selectedResource === 'custom' ? 'border-primary' : 'border-muted-foreground/40'
                                            }`}>
                                                {selectedResource === 'custom' && <span className="h-2 w-2 rounded-full bg-primary" />}
                                            </span>
                                            <span className="w-16 shrink-0 text-sm font-medium">Custom</span>
                                            <span className="text-xs text-muted-foreground">set CPU &amp; RAM</span>
                                        </button>
                                        {selectedResource === 'custom' && (
                                            <div className="flex items-center gap-4 px-3 pb-2.5 pl-[2.4rem]">
                                                <div className="flex items-center gap-1.5">
                                                    <input
                                                        type="number"
                                                        min="0.5"
                                                        step="0.5"
                                                        className="flex h-8 w-16 rounded-md border border-input bg-background px-2 py-1 text-xs ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                                                        placeholder="2"
                                                        value={customCpu}
                                                        onChange={(e) => setCustomCpu(e.target.value)}
                                                    />
                                                    <span className="text-xs text-muted-foreground">
                                                        vCPU{resourceConfig.max_cpu && ` (max ${coresFromQuantity(resourceConfig.max_cpu)})`}
                                                    </span>
                                                </div>
                                                <div className="flex items-center gap-1.5">
                                                    <input
                                                        type="number"
                                                        min="1"
                                                        step="1"
                                                        className="flex h-8 w-16 rounded-md border border-input bg-background px-2 py-1 text-xs ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                                                        placeholder="4"
                                                        value={customMemory}
                                                        onChange={(e) => setCustomMemory(e.target.value)}
                                                    />
                                                    <span className="text-xs text-muted-foreground">
                                                        GB{resourceConfig.max_memory && ` (max ${gbFromQuantity(resourceConfig.max_memory)})`}
                                                    </span>
                                                </div>
                                            </div>
                                        )}
                                    </div>
                                )}
                            </div>
                            {runningSize ? (
                                <p className="text-[10px] text-amber-600 dark:text-amber-400">
                                    Your kernel is running at {runningSize.cores} vCPU · {runningSize.memGB.toFixed(1)} GB.
                                    Picking a different size restarts it — all notebooks using this kernel lose their variables.
                                </p>
                            ) : (
                                <p className="text-[10px] text-muted-foreground">
                                    Changing size restarts the kernel.
                                </p>
                            )}
                        </div>
                    )}

                    {/* Spark Packages / JARs */}
                    <div className="space-y-2">
                        <Label className="text-sm font-medium">Spark JARs / Packages (Optional)</Label>
                        <Accordion type="single" collapsible defaultValue="package-presets" className="rounded-md border border-border/70 bg-muted/30 px-3">
                            <AccordionItem value="package-presets" className="border-none">
                                <AccordionTrigger className="py-3 text-xs font-medium hover:no-underline">
                                    Package Presets
                                </AccordionTrigger>
                                <AccordionContent className="space-y-2">
                                    <div className="flex flex-wrap gap-2">
                                        {PACKAGE_PRESETS.map((preset) => (
                                            <Button
                                                key={preset.id}
                                                type="button"
                                                variant={activePresetId === preset.id ? 'default' : 'outline'}
                                                size="sm"
                                                className="h-7 px-2 text-xs"
                                                onClick={() => applyPreset(preset)}
                                            >
                                                {preset.label}
                                            </Button>
                                        ))}
                                    </div>
                                    <div className="space-y-1">
                                        {PACKAGE_PRESETS.map((preset) => (
                                            <div key={`${preset.id}-hint`} className="text-[10px] text-muted-foreground">
                                                <span className="font-medium text-foreground">{preset.label}:</span> {preset.description}
                                            </div>
                                        ))}
                                    </div>
                                </AccordionContent>
                            </AccordionItem>
                        </Accordion>
                        <div className="space-y-1.5">
                            {packageRows.map((pkg, index) => (
                                <div key={`kernel-package-${index}`} className="flex items-center gap-2">
                                    <input
                                        className="flex h-9 w-full rounded-md border border-input bg-background px-2 py-1 text-xs font-mono ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                                        placeholder={language === 'python' ? 'org.apache.hadoop:hadoop-aws:3.3.4' : 'org.apache.spark:spark-avro_2.12:3.5.0'}
                                        value={pkg}
                                        onChange={(e) => updatePackageRow(index, e.target.value)}
                                    />
                                    {pkg.trim() && (
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="icon"
                                            className="h-8 w-8 shrink-0 text-muted-foreground"
                                            onClick={() => removePackageRow(index)}
                                        >
                                            <X className="h-4 w-4" />
                                        </Button>
                                    )}
                                </div>
                            ))}
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                className="h-7 px-2 text-xs text-primary"
                                onClick={addPackageRow}
                            >
                                <Plus className="mr-1 h-3.5 w-3.5" />
                                Add package
                            </Button>
                        </div>
                        <p className="text-[10px] text-muted-foreground">
                            {language === 'python'
                                ? 'Presets append to the list. You can still add more Maven coordinates manually.'
                                : 'Presets append to the list. You can still add more Maven coordinates manually; Scala packages will be auto-converted to Ammonite $ivy format.'
                            }
                        </p>
                    </div>

                    {packageListFromInput(sparkPackages).some(pkg => pkg.includes('org.apache.iceberg:iceberg-spark-runtime')) && (
                        <div className="space-y-2">
                            <Label className="text-sm font-medium">Iceberg Warehouse Path</Label>
                            <input
                                className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                                placeholder="s3a://your-bucket/iceberg_warehouse"
                                value={icebergWarehousePath}
                                onChange={(e) => setIcebergWarehousePath(e.target.value)}
                            />
                            <p className="text-[10px] text-muted-foreground">
                                Required for Iceberg. Example: <code>s3a://your-bucket/iceberg_warehouse</code>. SparkLabX will auto-configure an <code>iceberg</code> Hadoop catalog behind the scenes using this warehouse root.
                            </p>
                            <p className="text-[10px] text-muted-foreground">
                                Use table names like <code>iceberg.default.user_table</code>.
                            </p>
                        </div>
                    )}
                </div>

                <DialogFooter className="border-t px-6 py-4">
                    <Button variant="outline" onClick={onClose}>
                        Cancel
                    </Button>
                    <Button onClick={handleSubmit} disabled={isSubmitting || !selectedKernelName}>
                        {isSubmitting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                        {isSubmitting ? 'Connecting...' : 'Connect'}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
