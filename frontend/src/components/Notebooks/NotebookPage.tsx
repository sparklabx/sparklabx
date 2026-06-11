/**
 * NotebookPage - Main notebook page using Backend API
 * Uses useNotebook hooks for persistence and execution
 */

import React, { useState, useCallback, useEffect, useMemo, useRef } from 'react';
import { flushSync } from 'react-dom';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { useAIContext } from '@/contexts/AIContext';
import { devLog } from '@/lib/debug';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import {
    Plus,
    Play,
    Square,
    Trash2,
    Loader2,
    MoreVertical,
    Eraser,
    Search,
    X,
    Power,
    Pencil,
    Package,
    Cloud,
    CloudOff,
} from 'lucide-react';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { useMonaco } from '@monaco-editor/react';
import { toast } from 'sonner';
import { MenuBar } from '@/components/layout/MenuBar';
import { exportNotebookAsHTML, importNotebook } from '@/services/notebookService';

import {
    useNotebook,
    useNotebookList,
    NotebookCell,
    notebookCache,
} from '@/hooks/useNotebook';
import {
    useJupyterKernel,
    CellOutput,
} from '@/hooks/useJupyterKernel';
import { useKernelCompletionProvider } from '@/hooks/useKernelCompletionProvider';
import { useKernelHoverProvider } from '@/hooks/useKernelHoverProvider';

import {
    NotebookLanguage,
    CellType,
    notebookService,
} from '@/services/notebookService';
import { getUserDataPath } from '@/services/notebookStorageService';

import { KernelConnectionDialog } from './KernelConnectionDialog';
import { SidebarFiles } from './SidebarFiles';
import { ConnectionStatusBadge } from './parts/ConnectionStatusBadge';
import { LanguageIcon } from './parts/LanguageIcon';
import { CellEditor } from './parts/CellEditor';
import { registerAllStaticProviders } from './monaco/registerStatic';
import { SidebarIconRail, SidebarTab } from './parts/SidebarIconRail';

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

// Main NotebookPage component
export default function NotebookPage() {
    const params = useParams();
    const navigate = useNavigate();
    const notebookId = params?.id as string | undefined;

    // AI Context for registering page data
    const { registerPageContext, unregisterPageContext } = useAIContext();

    // Register Monaco static completion providers (Python/Scala/SQL) once on mount.
    useEffect(() => { registerAllStaticProviders(); }, []);

    // Hooks
    const {
        notebook,
        loading,
        error,
        loadNotebook,
        addCell,
        updateCell,
        deleteCell,
        updateNotebook,
        setNotebook,
    } = useNotebook(notebookId);

    // Track notebook via ref so effect closures can see the latest cells
    // without being re-run on every notebook mutation.
    const notebookRef = useRef(notebook);
    notebookRef.current = notebook;

    // Register Page Context (Cells) to AI Assistant. The getter reads
    // notebookRef at call time, so registering once per notebook is enough —
    // no need to re-register (and rebuild the cells payload) on every edit.
    useEffect(() => {
        registerPageContext(() => {
            const nb = notebookRef.current;
            return {
                type: 'notebook',
                notebookId,
                language: nb?.language,
                cells: (nb?.cells || []).map(c => ({
                    id: c.id,
                    type: c.type,
                    source: c.source,
                    outputs: (c.last_output?.output as CellOutput[] | undefined)?.slice(0, 3), // Limit outputs to avoid large payloads
                })),
            };
        });

        return () => {
            unregisterPageContext();
        };
    }, [notebookId, registerPageContext, unregisterPageContext]);

    const {
        connectionStatus,
        deadReason,  // Reason for kernel death when status is 'dead'
        podPhase,
        podMessage,
        cellOutputs,
        runningCells,
        runningCellStarts,
        executedCells,
        connect,
        checkConnection,
        disconnect,
        markDisconnecting,
        restart,
        trackPodStatus,
        executeCell,
        executeAllCells,
        pendingCells,
        executionCounts,
        executionTimes,
        restoreOutputs,
        clearCellOutput,
        waitForReady,
        requestCompletion,
        requestInspection,
    } = useJupyterKernel(notebookId || '');

    // Mirror live kernel state into refs (assigned during render, same
    // pattern as notebookRef above) so long-lived closures — the autosave
    // interval, waitForSparkInitCompletion's polling loop, pagehide flush —
    // always read fresh state without forcing those effects to re-subscribe
    // on every kernel message.
    const runningCellsRef = useRef(runningCells);
    runningCellsRef.current = runningCells;
    const cellOutputsRef = useRef(cellOutputs);
    cellOutputsRef.current = cellOutputs;
    const executedCellsRef = useRef(executedCells);
    executedCellsRef.current = executedCells;
    const executionCountsRef = useRef(executionCounts);
    executionCountsRef.current = executionCounts;
    const executionTimesRef = useRef(executionTimes);
    executionTimesRef.current = executionTimes;

    // Local state
    const [saveStatus, setSaveStatus] = useState<'saved' | 'saving' | 'unsaved'>('saved');
    const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
    const [notebookToDelete, setNotebookToDelete] = useState<{ id: string; name: string } | null>(null);
    const [disconnectConfirmOpen, setDisconnectConfirmOpen] = useState(false);
    const [renameTarget, setRenameTarget] = useState<{ id: string; name: string } | null>(null);
    const [renameValue, setRenameValue] = useState('');
    const [libraryDialogOpen, setLibraryDialogOpen] = useState(false);
    const [libraryInput, setLibraryInput] = useState('');
    const libraryRows = libraryInput ? libraryInput.split('\n') : [''];
    const [sparkInitPending, setSparkInitPending] = useState(false);
    const [sparkInitLogsOpen, setSparkInitLogsOpen] = useState(false);

    // Monaco instance for global registration
    const monaco = useMonaco();
    const notebookLanguage = ((notebook?.language || 'python').toLowerCase()) as 'python' | 'scala';

    // Kernel-backed completion + hover providers (delegate to hooks)
    useKernelCompletionProvider(monaco, requestCompletion, notebookRef);
    useKernelHoverProvider(monaco, notebookLanguage === 'python' ? requestInspection : undefined, notebookRef);

    // Use AI context to determine compact mode
    const { aiPanelOpen } = useAIContext();
    const compactToolbar = aiPanelOpen;

    // Consistent toolbar button classes
    const toolbarBtnBase = 'h-8 text-xs flex items-center justify-center gap-1';
    const toolbarBtnCompact = compactToolbar ? 'w-8 p-0' : 'px-3';

    // Kernel Connection Dialog state (unified)
    const [kernelDialogOpen, setKernelDialogOpen] = useState(false);
    // Packages live in state (not ref) so the auto-init effect re-runs when they change.
    // Hydrated from notebook.cluster_config below; previously a ref that started undefined,
    // which caused auto-reconnect to silently drop saved packages on page reload.
    const [sparkPackages, setSparkPackages] = useState<string | undefined>(undefined);
    const [icebergWarehousePath, setIcebergWarehousePath] = useState<string | undefined>(undefined);

    // Hydrate saved packages from the notebook's cluster_config when the notebook
    // loads (or changes). Without this, auto-reconnect builds the Spark session
    // without .config("spark.jars.packages", ...) and libraries are lost.
    useEffect(() => {
        const saved = (notebook as any)?.cluster_config?.['spark.jars.packages'];
        if (typeof saved === 'string' && saved.trim() !== '') {
            setSparkPackages(saved);
        } else {
            setSparkPackages(undefined);
        }
        const savedWarehouse = (notebook as any)?.cluster_config?.['spark.sql.catalog.iceberg.warehouse'];
        if (typeof savedWarehouse === 'string' && savedWarehouse.trim() !== '') {
            setIcebergWarehousePath(savedWarehouse);
        } else {
            setIcebergWarehousePath(undefined);
        }
    }, [notebook?.id]);

    // Auto-init Spark + (optionally) set fs.defaultFS to user's bucket when kernel connects.
    // Spark init runs regardless of storage path — K8s on-prem has no S3 bucket
    // but still needs a Spark session.
    useEffect(() => {
        if (connectionStatus !== 'connected' || !notebookId) return;
        let cancelled = false;

        // Skip re-init when the kernel pod is the SAME one we already
        // initialized in a previous tab/session. Spark context lives
        // inside the pod, so a tab reload (or laptop sleep/wake) that
        // reconnects to the same kernel_id needs no re-init — running
        // it again clobbers user state and shows a spurious "Booting
        // Spark…" badge. The localStorage flag is keyed per-notebook
        // and stores the kernel_id whose init we observed complete;
        // if the kernel restarts (manual restart or pod respawn), the
        // new kernel_id won't match and init runs again as normal.
        const currentKernelId = localStorage.getItem(`sparklabx_kernel_${notebookId}`);
        const initedKernelId = localStorage.getItem(`sparklabx_spark_inited_${notebookId}`);
        if (currentKernelId && currentKernelId === initedKernelId) {
            devLog(`[NotebookPage] Skipping Spark init — kernel ${currentKernelId} already initialized`);
            setSparkInitPending(false);
            return;
        }

        void (async () => {
            // Block user execution immediately on connect; init may take a while (first run downloads jars).
            setSparkInitPending(true);

            // If a previous kernel session left outputs for init-spark-context around,
            // `waitForSparkInitCompletion()` could incorrectly return "ready" immediately.
            // Clear it so we only observe outputs from THIS init attempt.
            clearCellOutput('init-spark-context');

            // Fetch optional path/endpoint, but do not block init on it.
            const pathInfo = await Promise.race([
                getUserDataPath().catch(() => null),
                new Promise<null>((resolve) => setTimeout(() => resolve(null), 1500)),
            ]);
            if (cancelled) return;

            const path = pathInfo?.path || '';
            const endpoint = pathInfo?.endpoint || '';
            const packages = sparkPackages;
            const packagesForSparkJars = packageListFromInput(packages).join(',');
            const icebergWarehouse = icebergWarehousePath;
            let code = '';

            devLog(`[NotebookPage] Generating init code. Packages: ${packages || 'None'}, path: ${path || '(none)'}, endpoint: ${endpoint || '(aws)'}`);

            // For custom S3 endpoints (MinIO / K8s on-prem), configure s3a to talk to it
            // with path-style access. Trailing "/" is stripped since s3a appends its own.
            const s3aEndpointConfigPy = endpoint
                ? ` \\
        .config("spark.hadoop.fs.s3a.endpoint", "${endpoint.replace(/\/$/, '')}") \\
        .config("spark.hadoop.fs.s3a.path.style.access", "true") \\
        .config("spark.hadoop.fs.s3a.connection.ssl.enabled", "${endpoint.startsWith('https') ? 'true' : 'false'}")`
                : '';

            // Resolve private + public storage roots. Path API returns both for the
            // new prefix model; old API only returns `path` (legacy = private).
            const publicPath = (pathInfo as any)?.public_path || '';

            if (notebookLanguage === 'python') {
                // Some packages need extra SparkSession config registered at build time
                // (not just the JAR on the classpath). Detect common ones and inject.
                const extraConfigs = new Map<string, string>();
                const pkgLower = (packages || '').toLowerCase();
                if (pkgLower.includes('io.delta:delta-spark') || pkgLower.includes('io.delta:delta-core')) {
                    extraConfigs.set('spark.sql.extensions', 'io.delta.sql.DeltaSparkSessionExtension');
                    extraConfigs.set('spark.sql.catalog.spark_catalog', 'org.apache.spark.sql.delta.catalog.DeltaCatalog');
                }
                if (pkgLower.includes('org.apache.iceberg:iceberg-spark-runtime')) {
                    const existing = extraConfigs.get('spark.sql.extensions');
                    extraConfigs.set(
                        'spark.sql.extensions',
                        existing
                            ? `${existing},org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions`
                            : 'org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions'
                    );
                    if (icebergWarehouse) {
                        extraConfigs.set('spark.sql.catalog.iceberg', 'org.apache.iceberg.spark.SparkCatalog');
                        extraConfigs.set('spark.sql.catalog.iceberg.type', 'hadoop');
                        extraConfigs.set('spark.sql.catalog.iceberg.warehouse', icebergWarehouse);
                    }
                }
                const extraConfigLines = Array.from(extraConfigs.entries())
                    .map(([k, v]) => ` \\
        .config("${k}", "${v}")`)
                    .join('');
                // Some Spark SQL configs are static (not modifiable after SparkSession starts),
                // notably `spark.sql.extensions`. Set these at build time only.
                const STATIC_SPARK_CONF_KEYS = new Set<string>([
                    'spark.sql.extensions',
                    'spark.sql.catalog.spark_catalog',
                    'spark.sql.catalog.iceberg',
                    'spark.sql.catalog.iceberg.type',
                    'spark.sql.catalog.iceberg.warehouse',
                ]);
                const runtimeConfigLinesPy = Array.from(extraConfigs.entries())
                    .filter(([k]) => !STATIC_SPARK_CONF_KEYS.has(k))
                    .map(([k, v]) => `    spark.conf.set("${k}", "${v}")`)
                    .join('\n');

                let sparkBuilder = `SparkSession.builder \\
        .appName("SparkLabX Session") \\
        .master("local[*]") \\
        .config("spark.driver.memory", "2g") \\
        .config("spark.executor.memory", "2g")${s3aEndpointConfigPy}${extraConfigLines}`;
                if (packagesForSparkJars) {
                    sparkBuilder += ` \\
        .config("spark.jars.packages", "${packagesForSparkJars}")`;
                }
                sparkBuilder += ` \\
        .getOrCreate()`;

                // Print the storage roots so users can paste them into spark.read.csv()
                // or use the sidebar's Copy Path button to get the exact URL.
                const pathBlock = path
                    ? `print('📂 Storage spaces:')
print('   • Private (R/W):  ${path}')${publicPath ? `\nprint('   • Public  (R):    ${publicPath}')` : ''}
print('   Use the Files sidebar 📋 Copy Path button to grab exact URLs.')
`
                    : '';

                code = `
import os
import traceback
from pyspark.sql import SparkSession, DataFrame as _SparkDF

${pathBlock}# Auto-Init Spark Session (Local)
try:
    spark = globals().get("spark", None)
    globals()["spark"] = spark
    if spark is None:
        print("🚀 Initializing Spark Session (Local)...")
        spark = ${sparkBuilder}
        globals()["spark"] = spark
        # Quiet noisy internal logs (Delta AddFile/Metadata spam, S3A status
        # dumps). Root via sparkContext; then force specific namespaces via
        # Configurator.setLevel which creates a LoggerConfig if missing.
        spark.sparkContext.setLogLevel("WARN")
        try:
            _Configurator = spark._jvm.org.apache.logging.log4j.core.config.Configurator
            _Level = spark._jvm.org.apache.logging.log4j.Level
            for _ns in ("io.delta",
                        "org.apache.spark.sql.delta",
                        "org.apache.hadoop",
                        "org.apache.hadoop.fs.s3a",
                        "org.apache.spark.sql.execution.datasources"):
                _Configurator.setLevel(_ns, _Level.WARN)
        except Exception:
            pass
        print(f"✅ Spark Session Initialized: {spark.version}")
    else:
        globals()["spark"] = spark
        print("Spark Session already active")

    # Pretty HTML rendering for DataFrames. Builds HTML straight from collect()
    # so we don't require pandas in the kernel image (toPandas pulls pandas).
    #   - df.show()              → HTML table, full data (HTML has horizontal
    #     scroll so we don't truncate by default like ASCII does). Pass an int
    #     to truncate explicitly: df.show(truncate=20). Original kept as _show_ascii.
    #   - df (last cell expr)    → HTML via _repr_html_ (Jupyter hook).
    #   - df.show(vertical=True) → ASCII fallback (HTML can't render vertically).
    from html import escape as _spx_esc
    from IPython.display import display as _spx_display, HTML as _SpxHTML
    def _spx_df_to_html(self, n=20, truncate=False):
        cols = self.schema.names
        rows = self.limit(n).collect()
        # truncate semantics: False/0/True → no cap (HTML scrolls horizontally);
        # int N → trim each cell to N chars + "...". Matches Spark's signature
        # for explicit ints, but defaults to "show everything" since the
        # narrow-terminal motivation behind Spark's default doesn't apply here.
        limit = int(truncate) if isinstance(truncate, int) and not isinstance(truncate, bool) else 0
        def _cell(v):
            if v is None:
                return "null"
            s = str(v)
            if limit > 0 and len(s) > limit:
                s = s[:max(1, limit - 3)] + "..."
            return _spx_esc(s)
        thead = "<tr>" + "".join(f"<th>{_spx_esc(c)}</th>" for c in cols) + "</tr>"
        tbody = "".join(
            "<tr>" + "".join(f"<td>{_cell(r[i])}</td>" for i in range(len(cols))) + "</tr>"
            for r in rows
        )
        note = f'<div style="color:#888;font-size:11px;margin-top:4px">showing first {len(rows)} rows</div>'
        return f'<table class="dataframe">{thead}{tbody}</table>{note}'
    _SparkDF._show_ascii = _SparkDF.show
    def _spx_show(self, n=20, truncate=False, vertical=False):
        if vertical:
            return self._show_ascii(n, truncate if isinstance(truncate, bool) else True, vertical)
        try:
            _spx_display(_SpxHTML(_spx_df_to_html(self, n, truncate)))
        except Exception:
            self._show_ascii(n, truncate if isinstance(truncate, bool) else True, vertical)
    _SparkDF.show = _spx_show
    _SparkDF._repr_html_ = lambda self: _spx_df_to_html(self, 50, False)
    # Databricks-style global helper: display(df, 5) → 5-row HTML table.
    # Falls through to IPython.display.display for non-DataFrame args so we
    # don't break code that expects the stock IPython behavior.
    def display(_obj, n=20, truncate=False):
        if isinstance(_obj, _SparkDF):
            _spx_display(_SpxHTML(_spx_df_to_html(_obj, n, truncate)))
        else:
            _spx_display(_obj)
    globals()["display"] = display

${runtimeConfigLinesPy ? `${runtimeConfigLinesPy}
` : ''}
    print("__SPARKLABX_SPARK_READY__")
except Exception as _e:
    spark = globals().get("spark", None)
    globals()["spark"] = spark
    print("❌ Spark initialization failed:")
    traceback.print_exc()
`;
            } else if (notebookLanguage === 'scala') {
                // Almond runs inside an existing JVM, so `spark.jars.packages` on the
                // SparkSession builder does NOT actually load JARs (that config is
                // spark-submit-only). Use Ammonite's `import $ivy.\`…\`` to resolve
                // the JAR via Coursier and splice it into the running classpath
                // BEFORE the kernel's lazy `spark` val is first touched.
                const ivyImports = packages
                    ? packages
                        .split(/[,\n]/)
                        .map(p => p.trim())
                        .filter(p => p.length > 0)
                        .map(p => `import $ivy.\`${p}\``)
                        .join('\n')
                    : '';

                // Extensions that must be registered at SparkSession build time.
                // Set via SparkConfig so the predef.sc builder picks them up when
                // the lazy `spark` val is accessed below.
                const extraConfigs = new Map<string, string>();
                const pkgLower = (packages || '').toLowerCase();
                if (pkgLower.includes('io.delta:delta-spark') || pkgLower.includes('io.delta:delta-core')) {
                    extraConfigs.set('spark.sql.extensions', 'io.delta.sql.DeltaSparkSessionExtension');
                    extraConfigs.set('spark.sql.catalog.spark_catalog', 'org.apache.spark.sql.delta.catalog.DeltaCatalog');
                }
                if (pkgLower.includes('org.apache.iceberg:iceberg-spark-runtime')) {
                    const existing = extraConfigs.get('spark.sql.extensions');
                    extraConfigs.set(
                        'spark.sql.extensions',
                        existing
                            ? `${existing},org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions`
                            : 'org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions'
                    );
                    if (icebergWarehouse) {
                        extraConfigs.set('spark.sql.catalog.iceberg', 'org.apache.iceberg.spark.SparkCatalog');
                        extraConfigs.set('spark.sql.catalog.iceberg.type', 'hadoop');
                        extraConfigs.set('spark.sql.catalog.iceberg.warehouse', icebergWarehouse);
                    }
                }
                const extraConfigBlock = Array.from(extraConfigs.entries())
                    .map(([k, v]) => `SparkConfig.set("${k}", "${v}")`)
                    .join('\n');
                // Some Spark SQL configs are static (not modifiable after SparkSession starts),
                // notably `spark.sql.extensions`. These must be set via SparkConfig before
                // the lazy `spark` val is touched; do NOT apply them via s.conf.set at runtime.
                const STATIC_SPARK_CONF_KEYS = new Set<string>([
                    'spark.sql.extensions',
                    'spark.sql.catalog.spark_catalog',
                    'spark.sql.catalog.iceberg',
                    'spark.sql.catalog.iceberg.type',
                    'spark.sql.catalog.iceberg.warehouse',
                ]);
                const runtimeConfigBlockScala = Array.from(extraConfigs.entries())
                    .filter(([k]) => !STATIC_SPARK_CONF_KEYS.has(k))
                    .map(([k, v]) => `s.conf.set("${k}", "${v}")`)
                    .join('\n');

                // Print storage roots. Users paste into spark.read.csv() or use the
                // sidebar's Copy Path button.
                const pathBlock = path
                    ? `println("📂 Storage spaces:")
println("   • Private (R/W):  ${path}")${publicPath ? `\nprintln("   • Public  (R):    ${publicPath}")` : ''}
println("   Use the Files sidebar 📋 Copy Path button to grab exact URLs.")`
                    : '';

                const s3aEndpointBlockScala = endpoint
                    ? `
spark.conf.set("spark.hadoop.fs.s3a.endpoint", "${endpoint.replace(/\/$/, '')}")
spark.conf.set("spark.hadoop.fs.s3a.path.style.access", "true")
spark.conf.set("spark.hadoop.fs.s3a.connection.ssl.enabled", "${endpoint.startsWith('https') ? 'true' : 'false'}")`
                    : '';

                code = `
${ivyImports}

import org.apache.spark.sql.SparkSession

${pathBlock}
${extraConfigBlock}

// Auto-Init Spark Session (Local).
// Reference the Almond kernel's built-in lazy 'spark' val — do not shadow it
// with SparkSession.builder.getOrCreate(), as that bypasses SparkConfig.
try {
    println("🚀 Initializing Spark Session...")
    val s = spark${s3aEndpointBlockScala}
    ${runtimeConfigBlockScala}
    // Quiet the root logger first (Spark's helper runs updateLoggers()),
    // then force specific namespaces via Configurator.setLevel which CREATES
    // a LoggerConfig if none exists and calls updateLoggers internally.
    try { s.sparkContext.setLogLevel("WARN") } catch { case _: Throwable => }
    try {
        import org.apache.logging.log4j.core.config.Configurator
        import org.apache.logging.log4j.Level
        Seq(
            "io.delta",
            "org.apache.spark.sql.delta",
            "org.apache.hadoop",
            "org.apache.hadoop.fs.s3a",
            "org.apache.spark.sql.execution.datasources"
        ).foreach(ns => Configurator.setLevel(ns, Level.WARN))
    } catch {
        case _: Throwable => // log4j API may differ across versions; ignore
    }
    println(s"✅ Spark Session Active: \${s.version}")
} catch {
    case e: Throwable =>
        println(s"❌ Spark initialization failed: \${e.getMessage}")
        e.printStackTrace()
}

// display(df) — pretty HTML table for Scala (.show() stays ASCII because
// Scala doesn't allow monkey-patching methods on existing classes).
// .toDF coerces typed Datasets to Row so .isNullAt / .get(i) are valid.
def display(df: org.apache.spark.sql.Dataset[_], n: Int = 20): Unit = {
    val asDf = df.toDF
    val rows = asDf.limit(n).collect()
    val cols = asDf.schema.fieldNames
    def esc(v: Any): String = Option(v).map(_.toString).getOrElse("null")
        .replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    val thead = "<tr>" + cols.map(c => s"<th>\${esc(c)}</th>").mkString + "</tr>"
    val tbody = rows.map(r => "<tr>" + cols.indices.map(i =>
        s"<td>\${if (r.isNullAt(i)) "null" else esc(r.get(i))}</td>"
    ).mkString + "</tr>").mkString
    val html = s"""<table class="dataframe">$thead$tbody</table><div style="color:#888;font-size:11px;margin-top:4px">showing first \${rows.length} rows</div>"""
    try { almond.display.Html(html).display() }
    catch { case _: Throwable => println(html) } // fallback if Almond API differs
}
`;
            }

            if (code) {
                devLog(`[NotebookPage] Injecting initialization code for ${notebookLanguage}`);
                // Prevent running cells before Spark init finishes (avoids NameError: spark is not defined).
                executeCell(
                    'init-spark-context',
                    code,
                    undefined,
                    { silent: false, storeHistory: false }
                );

                const initTimeoutMs = packages ? 300000 : 180000; // allow extra time for first-time jar downloads
                let ok = await waitForSparkInitCompletion(initTimeoutMs);
                if (!ok && !cancelled) {
                    const stillRunning = runningCellsRef.current.has('init-spark-context');
                    if (stillRunning) {
                        toast.warning('Spark is still initializing…', { description: 'This can take a few minutes the first time.' });
                        // Give it one more longer window in the background without spamming toasts.
                        ok = await waitForSparkInitCompletion(300000);
                    }
                }

                if (!cancelled) {
                    const stillRunning = runningCellsRef.current.has('init-spark-context');
                    // If still running, keep the initializing indicator on.
                    setSparkInitPending(!ok && stillRunning);
                    if (!ok && !stillRunning) toast.error('Spark initialization timed out');
                    if (ok) {
                        setSparkInitPending(false);
                        // Remember which kernel pod we successfully
                        // initialized so a tab reload (same pod)
                        // skips re-init.
                        const k = localStorage.getItem(`sparklabx_kernel_${notebookId}`);
                        if (k) localStorage.setItem(`sparklabx_spark_inited_${notebookId}`, k);
                    }
                }
            }
        })();
        return () => { cancelled = true; };
    }, [connectionStatus, notebookId, notebookLanguage, sparkPackages, icebergWarehousePath, executeCell, clearCellOutput]);




    // Multi-tab detection — warn if same notebook open in another tab
    useEffect(() => {
        if (!notebookId) return;
        const channel = new BroadcastChannel(`notebook-${notebookId}`);
        // Announce this tab
        channel.postMessage({ type: 'open', tabId: Date.now() });
        const handler = (e: MessageEvent) => {
            if (e.data?.type === 'open') {
                toast.warning('This notebook is open in another tab. Edits may conflict.', { duration: 5000 });
            }
        };
        channel.addEventListener('message', handler);
        return () => { channel.removeEventListener('message', handler); channel.close(); };
    }, [notebookId]);

    // Listen for notebook updates from AI Agent
    useEffect(() => {
        const handleNotebookUpdate = () => {
            if (notebookId) {
                loadNotebook(notebookId);
            }
        };

        window.addEventListener('notebook-updated', handleNotebookUpdate);
        return () => window.removeEventListener('notebook-updated', handleNotebookUpdate);
    }, [notebookId, loadNotebook]);

    // Reload outputs when kernel reconnects (WS dropped, outputs may be stale)
    useEffect(() => {
        const handler = (e: Event) => {
            const detail = (e as CustomEvent).detail;
            if (detail?.notebookId === notebookId && notebookId) {
                notebookCache.delete(notebookId);
                loadNotebook(notebookId);
            }
        };
        window.addEventListener('kernel-reconnected', handler);
        return () => window.removeEventListener('kernel-reconnected', handler);
    }, [notebookId, loadNotebook]);

    // Sidebar state
    const [sidebarTab, setSidebarTabRaw] = useState<'toc' | 'workspace' | 'catalog' | 'settings' | 'files'>(() => {
        const saved = localStorage.getItem('sparklabx-sidebar-tab');
        return (['toc', 'workspace', 'catalog', 'settings', 'files'].includes(saved || '') ? saved : 'workspace') as any;
    });
    const setSidebarTab = (tab: typeof sidebarTab) => {
        setSidebarTabRaw(tab);
        localStorage.setItem('sparklabx-sidebar-tab', tab);
    };
    const [sidebarOpen, setSidebarOpen] = useState(true);
    const [searchQuery, setSearchQuery] = useState('');

    // Fetch notebooks list for sidebar
    const { notebooks: allNotebooks, loading: notebooksLoading, loadNotebooks, createNotebook, deleteNotebook } = useNotebookList();

    // Auto-redirect to first notebook if no ID provided
    useEffect(() => {
        if (notebooksLoading || allNotebooks.length === 0) return;
        if (!notebookId) {
            navigate(`/notebooks/${allNotebooks[0].id}`, { replace: true });
        }
    }, [notebookId, notebooksLoading, allNotebooks, navigate]);

    // Catalog feature removed in notebook-lite — no Lakekeeper integration.

    // Handlers
    const handleAddCell = async (type: CellType, afterOrder?: number) => {
        const newCell = await addCell(type, '', afterOrder);
        if (newCell) {
            // Focus new cell after React renders it
            setTimeout(() => {
                const cellEl = document.querySelector(`[data-cell-id="${newCell.id}"]`);
                if (cellEl) {
                    cellEl.scrollIntoView({ behavior: 'smooth', block: 'center' });
                    // Focus the Monaco editor inside the cell
                    const editor = cellEl.querySelector('.monaco-editor textarea, .monaco-editor .inputarea') as HTMLElement;
                    if (editor) editor.focus();
                }
            }, 100);
        }
    };

    const handleConnect = async () => {
        // Block if a previous pod is still shutting down — opening the dialog
        // and clicking through would just trigger backend's 202-terminating loop
        // and confuse the user. Tell them to wait.
        if (podPhase === 'terminating') {
            toast.info(podMessage || 'Previous kernel is still shutting down, please wait…');
            return;
        }
        setKernelDialogOpen(true);
    };

    const waitForSparkInitCompletion = async (timeoutMs: number = 180000): Promise<boolean> => {
        const start = Date.now();
        let sawInitRunning = false;

        while (Date.now() - start < timeoutMs) {
            const running = runningCellsRef.current;
            const initOutputs = cellOutputsRef.current['init-spark-context'];
            const isRunning = running.has('init-spark-context');

            if (isRunning) {
                sawInitRunning = true;
            }

            const flatText = (initOutputs || [])
                .map((o: any) => (o?.text ? String(o.text) : JSON.stringify(o?.data ?? '')))
                .join('\n');

            if (flatText.includes('__SPARKLABX_SPARK_READY__') ||
                flatText.includes('✅ Spark Session Initialized') ||
                flatText.includes('✅ Spark Session Active')) {
                return true;
            }

            if (flatText.includes('❌ Spark initialization failed:')) {
                return false;
            }

            // Fallback: if we saw init running and it finished with outputs, treat as done.
            // (This keeps UX responsive even if kernels change their output format.)
            if (!isRunning && sawInitRunning && (initOutputs && initOutputs.length > 0)) {
                return true;
            }

            await new Promise(resolve => setTimeout(resolve, 200));
        }

        return false;
    };

    const getStoredKernelProfile = () => ({
        sparkPackages: ((notebook as any)?.cluster_config?.['spark.jars.packages'] as string | undefined) || undefined,
        icebergWarehousePath: ((notebook as any)?.cluster_config?.['spark.sql.catalog.iceberg.warehouse'] as string | undefined) || undefined,
    });

    const handleKernelDialogConnect = async (options: {
        kernelName?: string;
        sparkPackages?: string;
        icebergWarehousePath?: string;
    }) => {
        if (!notebook) return;
        const existingProfile = getStoredKernelProfile();
        const nextIcebergWarehousePath = options.icebergWarehousePath ?? existingProfile.icebergWarehousePath;

        try {
            try {
                const nextClusterConfig: Record<string, string | number> = {};
                if (options.sparkPackages) {
                    nextClusterConfig['spark.jars.packages'] = options.sparkPackages;
                }
                if (nextIcebergWarehousePath) {
                    nextClusterConfig['spark.sql.catalog.iceberg.warehouse'] = nextIcebergWarehousePath;
                }
                await updateNotebook({ cluster_config: nextClusterConfig });
                devLog('[NotebookPage] Saved spark packages to notebook config');
            } catch (e) {
                console.error('[NotebookPage] Failed to save config:', e);
                // Don't block connection if save fails.
            }

            const lang = (notebook.language || 'python').toLowerCase() as NotebookLanguage;
            devLog('[NotebookPage] Connecting kernel:', { language: lang, kernelName: options.kernelName });

            // Push packages into state BEFORE connect(). The auto-init effect
            // reads this state and injects init code once status === 'connected'.
            // Previously this flow injected its own init code here too, which
            // raced with the auto-init effect and used a divergent template
            // (Python: no `if 'spark' not in locals()` guard; Scala: $ivy +
            // shadowing the kernel's lazy `spark` val). Single path now.
            setSparkPackages(options.sparkPackages);
            setIcebergWarehousePath(nextIcebergWarehousePath);
            connect(lang, options.kernelName);

            // Wait for kernel to be fully ready (max 60s). The auto-init effect
            // handles init code injection once the kernel reports connected.
            devLog('[NotebookPage] Waiting for kernel to be ready...');
            const isReady = await waitForReady(60000);

            if (!isReady) {
                toast.error('Kernel failed to connect or timed out');
                return;
            }

            devLog('[NotebookPage] Waiting for Spark init to complete...');
            const sparkInitReady = await waitForSparkInitCompletion();
            if (!sparkInitReady) {
                toast.error('Spark initialization timed out');
                return;
            }

            devLog('[NotebookPage] Kernel ready!');
            toast.success('Kernel connected');
        } catch (error) {
            console.error('Failed to connect:', error);
            toast.error('Connection failed');
        }
    };

    // Auto-restart Scala kernel if Almond's semanticdb tooling crashes during hover/inspect.
    // This keeps IDE features enabled without spamming users with stacktraces.
    const lastToolingRestartAtRef = useRef<number>(0);
    useEffect(() => {
            const handler = async (e: Event) => {
                const detail = (e as CustomEvent).detail;
                if (!detail || detail?.notebookId !== notebookId) return;
            if (detail?.kind !== 'scala-tooling') return;
                if (!notebookId || !notebook) return;
                if (notebookLanguage !== 'scala') return;
                if (runningCells.size > 0) return;

            const now = Date.now();
            if (now - lastToolingRestartAtRef.current < 60000) return; // throttle
            lastToolingRestartAtRef.current = now;

            try {
                toast.info('Restarting Scala kernel (tooling crash)…', { duration: 5000 });
                // Fast in-pod restart — pod and libraries preserved.
                await restart();
            } catch {
                toast.error('Failed to restart Scala kernel');
            }
        };
        window.addEventListener('kernel-tooling-crash', handler);
        return () => window.removeEventListener('kernel-tooling-crash', handler);
    }, [notebookId, notebook, notebookLanguage, runningCells.size, restart]);

    const handleDisconnect = () => {
        setDisconnectConfirmOpen(true);
    };

    const confirmDisconnect = async () => {
        // Force the dialog close to commit in its own React batch
        // BEFORE the disconnect() status updates. Without flushSync
        // React 18 automatic batching merges the dialog close and
        // the 'disconnecting' status into one commit, and the
        // intermediate state never paints — the badge appears to
        // stay on "Connected" through the dialog fade-out.
        flushSync(() => {
            setDisconnectConfirmOpen(false);
        });
        await disconnect(); // disconnect returns void, handles errors internally

        // Clear from DB/Cache if needed
        await updateNotebook({ cluster_config: {} });
    };

    const handleDeleteNotebook = async (id: string, name: string) => {
        setNotebookToDelete({ id, name });
        setDeleteConfirmOpen(true);
    };

    // Create notebook dialog state
    const [createDialogOpen, setCreateDialogOpen] = useState(false);
    const [createLanguage, setCreateLanguage] = useState<'python' | 'scala'>('python');
    const [createName, setCreateName] = useState('');
    const [createError, setCreateError] = useState('');

    const openCreateDialog = (language: 'python' | 'scala') => {
        const baseName = language === 'scala' ? 'New Scala Notebook' : 'New Python Notebook';
        let defaultName = baseName;
        const existingNames = allNotebooks.map(n => n.name);
        if (existingNames.includes(defaultName)) {
            let i = 1;
            while (existingNames.includes(`${baseName} (${i})`)) i++;
            defaultName = `${baseName} (${i})`;
        }
        setCreateLanguage(language);
        setCreateName(defaultName);
        setCreateError('');
        setCreateDialogOpen(true);
    };

    const confirmCreateNotebook = async () => {
        const name = createName.trim();
        if (!name) { setCreateError('Name is required'); return; }
        if (allNotebooks.some(n => n.name === name)) {
            setCreateError('A notebook with this name already exists');
            return;
        }
        const newNb = await createNotebook({
            name,
            language: createLanguage,
        });
        setCreateDialogOpen(false);
        if (newNb) {
            navigate(`/notebooks/${newNb.id}`);
        }
    };

    const confirmDeleteNotebook = async () => {
        if (!notebookToDelete) return;

        const success = await deleteNotebook(notebookToDelete.id);
        if (success) {
            // Clear from cache
            notebookCache.delete(notebookToDelete.id);

            // If deleting current notebook, switch to another one
            if (notebookToDelete.id === notebookId) {
                const remainingNotebooks = allNotebooks.filter(n => n.id !== notebookToDelete.id);
                if (remainingNotebooks.length > 0) {
                    navigate(`/notebooks/${remainingNotebooks[0].id}`);
                } else {
                    navigate('/notebooks');
                }
            }
        }

        setDeleteConfirmOpen(false);
        setNotebookToDelete(null);
    };

    // Auto-connect when notebook is loaded
    useEffect(() => {
        if (notebook && connectionStatus === 'disconnected') {
            // Reconnect to existing kernel only if it matches notebook language
            const lang = (notebook.language || 'python').toLowerCase();
            const expectedKernelName = lang === 'scala' ? 'scala212' : 'pyspark';
            checkConnection(expectedKernelName);
        }
    }, [notebook?.id]);


    // Restore saved outputs from database when notebook loads (RUN ONCE)
    const notebookLoadedRef = useRef<string | null>(null);
    useEffect(() => {
        if (notebook?.id && notebook.cells?.length > 0) {
            // Only restore once on initial load
            if (notebook.id !== notebookLoadedRef.current) {
                notebookLoadedRef.current = notebook.id;

                const savedOutputs: Record<string, any> = {};
                const savedCounts: Record<string, number> = {};
                notebook.cells.forEach(cell => {
                    if (cell.last_output) {
                        savedOutputs[cell.id] = cell.last_output;
                    }
                    if (cell.execution_count != null) {
                        savedCounts[cell.id] = cell.execution_count;
                    }
                });

                if (Object.keys(savedOutputs).length > 0 || Object.keys(savedCounts).length > 0) {
                    restoreOutputs(savedOutputs, savedCounts);
                }
            }
        }
    }, [notebook, restoreOutputs]);

    // Debounce cell updates - prevent API spam on every keystroke
    const updateCellTimerRef = useRef<{ [cellId: string]: NodeJS.Timeout }>({});

    // Cleanup timers on unmount
    // ============ Periodic Autosave Logic ============

    // Serialization queue for cell updates to prevent backend race conditions
    const saveQueueRef = useRef<Promise<void>>(Promise.resolve());

    const queuedUpdateCell = useCallback((cellId: string, updates: any) => {
        // Enqueue the update
        saveQueueRef.current = saveQueueRef.current.then(async () => {
            // Artificial delay to prevent API flooding? No, we want serialization.
            try {
                await updateCell(cellId, updates);
            } catch (error) {
                console.error('[NotebookPage] Failed to save cell output:', error);
            }
        });
        return saveQueueRef.current;
    }, [updateCell]);

    // Track cells that need saving (memory as source of truth)
    const dirtyCellIdsRef = useRef<Set<string>>(new Set());
    const prevCellOutputs = useRef<Record<string, CellOutput[]>>({});
    const prevExecutedCellsRef = useRef<Set<string>>(new Set());

    useEffect(() => {
        return () => {
            Object.values(updateCellTimerRef.current).forEach(timer => clearTimeout(timer));
        };
    }, []);

    const handleUpdateCell = useCallback((cellId: string, source: string) => {
        setSaveStatus('unsaved');
        // Update local state immediately for smooth typing experience
        setNotebook(prev => prev ? {
            ...prev,
            cells: prev.cells.map(c => c.id === cellId ? { ...c, source } : c)
        } : null);

        // Clear existing timer for this cell
        if (updateCellTimerRef.current[cellId]) {
            clearTimeout(updateCellTimerRef.current[cellId]);
        }

        // Debounce: save after 5s of no typing
        updateCellTimerRef.current[cellId] = setTimeout(async () => {
            setSaveStatus('saving');
            await updateCell(cellId, { source });
            setSaveStatus('saved');
        }, 5000);
    }, [updateCell]);

    const handleDeleteCell = async (cellId: string) => {
        await deleteCell(cellId);
    };

    const handleMoveCell = (cellId: string, direction: 'up' | 'down') => {
        // Read from ref (not closure) — CellEditor's memo may hold stale callbacks,
        // so `notebook` from closure can be an old snapshot on consecutive clicks.
        const current = notebookRef.current;
        if (!current) return;

        const notebookId = current.id;

        const sortedCells = [...current.cells].sort((a, b) => a.order - b.order);
        const cellIndex = sortedCells.findIndex(c => c.id === cellId);
        if (cellIndex === -1) return;

        const swapIndex = direction === 'up' ? cellIndex - 1 : cellIndex + 1;
        if (swapIndex < 0 || swapIndex >= sortedCells.length) return;

        const newSortedCells = [...sortedCells];
        [newSortedCells[cellIndex], newSortedCells[swapIndex]] = [newSortedCells[swapIndex], newSortedCells[cellIndex]];
        const newCellIds = newSortedCells.map(c => c.id);

        setNotebook(prev => {
            if (!prev) return null;
            return {
                ...prev,
                cells: prev.cells.map(c => ({
                    ...c,
                    order: newCellIds.indexOf(c.id)
                }))
            };
        });

        if (!newCellIds.some(id => id.startsWith('temp-'))) {
            localStorage.setItem(`notebook-order-${notebookId}`, JSON.stringify(newCellIds));
            notebookService.reorderCells(notebookId, newCellIds)
                .catch(err => console.error('Failed to persist cell order:', err));
        }
    };

    // Autosave interval. Reads all live state through refs so the interval
    // and the pagehide listener are registered once per notebook instead of
    // being torn down and re-created on every kernel output flush.
    useEffect(() => {
        const AUTOSAVE_INTERVAL = 5000;

        const saveDirtyCells = async () => {
            if (dirtyCellIdsRef.current.size === 0) return;

            setSaveStatus('saving');

            // Snapshot current dirty set and clear it
            const cellsToSave = Array.from(dirtyCellIdsRef.current);
            dirtyCellIdsRef.current.clear();

            // Queue updates effectively
            for (const cellId of cellsToSave) {
                // Skip transient cells (e.g. Spark Connect init cells) that don't exist in DB
                const cellExists = notebookRef.current?.cells?.some(c => c.id === cellId);
                if (!cellExists) {
                    devLog(`[Autosave] Skipping transient cell ${cellId}`);
                    continue;
                }

                const outputs = cellOutputsRef.current[cellId] || [];
                const isExecuted = executedCellsRef.current.has(cellId);
                const execCount = executionCountsRef.current[cellId];
                const execTime = executionTimesRef.current[cellId];

                // Use existing queue to ensure serialization. Persist
                // last_execution_time_ms so the badge survives a page
                // reload — without it the in-session executionTimes map
                // is lost and the cell goes back to showing no duration.
                queuedUpdateCell(cellId, {
                    last_output: {
                        outputs: outputs,
                        executed: isExecuted
                    },
                    ...(execCount != null ? { execution_count: execCount } : {}),
                    ...(execTime != null ? { last_execution_time_ms: Math.round(execTime) } : {}),
                });
            }
            setSaveStatus('saved');
        };

        const intervalId = setInterval(saveDirtyCells, AUTOSAVE_INTERVAL);

        // Flush pending source debounce timers on unload via sync XHR.
        // No interrupt-on-leave: the backend KernelRecorder keeps writing
        // cell.last_output as the kernel emits messages, so closing the
        // tab no longer drops in-flight output.
        const handlePageHide = () => {
            if (notebookRef.current) {
                Object.entries(updateCellTimerRef.current).forEach(([cellId, timer]) => {
                    clearTimeout(timer);
                    const cell = notebookRef.current?.cells.find(c => c.id === cellId);
                    if (cell) {
                        const xhr = new XMLHttpRequest();
                        xhr.open('PUT', `/api/v1/notebooks/${notebookRef.current!.id}/cells/${cellId}`, false); // sync
                        xhr.setRequestHeader('Content-Type', 'application/json');
                        const token = localStorage.getItem('sparklabx_token');
                        if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`);
                        try { xhr.send(JSON.stringify({ source: cell.source })); } catch { /* best effort */ }
                    }
                });
                updateCellTimerRef.current = {};
            }
            saveDirtyCells();
        };
        window.addEventListener('pagehide', handlePageHide);

        return () => {
            clearInterval(intervalId);
            window.removeEventListener('pagehide', handlePageHide);
            saveDirtyCells();
        };
    }, [queuedUpdateCell]);

    // Track dirty state when outputs change
    useEffect(() => {
        Object.keys(cellOutputs).forEach(cellId => {
            if (cellOutputs[cellId] !== prevCellOutputs.current[cellId]) {
                dirtyCellIdsRef.current.add(cellId);
            }
        });

        executedCells.forEach(cellId => {
            if (!prevExecutedCellsRef.current?.has(cellId)) {
                dirtyCellIdsRef.current.add(cellId);
            }
        });

        prevCellOutputs.current = cellOutputs;
        prevExecutedCellsRef.current = executedCells;
    }, [cellOutputs, executedCells]);

    // Ctrl+S / Cmd+S placeholder — wired after handleSave
    const handleSaveRef = useRef<() => void>(() => {});

    const handleRunCell = async (cellId: string, source: string) => {
        // Kernel is running the Spark init cell — queuing user cells during this
        // window can produce confusing "success with no output" results.
        if (sparkInitPending || runningCells.has('init-spark-context')) {
            toast.info('Kernel is still initializing Spark, please wait a few seconds...');
            return;
        }

        // Flush pending cell update immediately (e.g. code changes)
        if (updateCellTimerRef.current[cellId]) {
            clearTimeout(updateCellTimerRef.current[cellId]);
            queuedUpdateCell(cellId, { source });
        }

        // NOTE: We DO NOT clear output in DB here anymore.
        // We rely on memory state (which is cleared by useJupyterKernel) to show "running" UI.
        // The empty output will be picked up by the dirty tracker and saved in next autosave.

        // executeCell will check connection status internally and handle errors
        executeCell(cellId, source);
    };

    const sparkInitOutputs = cellOutputs['init-spark-context'] || [];
    const sparkInitLogText = sparkInitOutputs.map((o: any) => {
        if (o?.type === 'stream' && typeof o?.text === 'string') return o.text;
        if (o?.type === 'error') {
            const tb = Array.isArray(o?.traceback) ? o.traceback.join('\n') : '';
            return [o?.ename, o?.evalue, tb].filter(Boolean).join('\n');
        }
        if (o?.type === 'result' && o?.data) return JSON.stringify(o.data, null, 2);
        return '';
    }).filter(Boolean).join('\n');

    // Interrupt whatever the kernel is currently executing. SIGINT travels to
    // Python (KeyboardInterrupt) / Almond (cancel cell) and Spark catches it to
    // cancel the active job. Kernel session + variables + cached DataFrames
    // survive — exactly the "stop this query, keep my state" gesture users
    // reach for after misclicking Run on the wrong cell.
    const handleInterrupt = async () => {
        if (!notebookId) return;
        try {
            await axios.post(`/api/v1/notebooks/${notebookId}/kernel/interrupt`);
            toast.success('Kernel interrupted');
        } catch (err) {
            const e = err as { response?: { data?: { error?: string } } };
            toast.error(e.response?.data?.error || 'Failed to interrupt kernel');
        }
    };

    const handleClearOutput = async (cellId: string) => {
        // Clear memory state immediately for UX
        clearCellOutput(cellId);

        // Also persist to DB via queue (0 is treated as empty by the display)
        queuedUpdateCell(cellId, {
            last_output: { outputs: [], executed: false },
            execution_count: 0,
        });
        devLog('[NotebookPage] Cleared output for cell', cellId);
    };

    const handleSave = async () => {
        setSaveStatus('saving');
        // Force flush pending source debounce timers
        Object.entries(updateCellTimerRef.current).forEach(([cellId, timer]) => {
            clearTimeout(timer);
            const cell = notebook?.cells.find(c => c.id === cellId);
            if (cell) updateCell(cellId, { source: cell.source });
        });
        updateCellTimerRef.current = {};
        // Force flush dirty output cells
        if (dirtyCellIdsRef.current.size > 0) {
            const cellsToSave = Array.from(dirtyCellIdsRef.current);
            dirtyCellIdsRef.current.clear();
            for (const cellId of cellsToSave) {
                const outputs = cellOutputs[cellId] || [];
                const isExecuted = executedCells.has(cellId);
                const execCount = executionCounts[cellId];
                queuedUpdateCell(cellId, {
                    last_output: { outputs, executed: isExecuted },
                    ...(execCount != null ? { execution_count: execCount } : {}),
                });
            }
        }
        await new Promise(resolve => setTimeout(resolve, 300));
        setSaveStatus('saved');
    };

    // Wire Ctrl+S / Cmd+S
    handleSaveRef.current = handleSave;
    useEffect(() => {
        const handler = (e: KeyboardEvent) => {
            if ((e.ctrlKey || e.metaKey) && e.key === 's') {
                e.preventDefault();
                handleSaveRef.current();
            }
        };
        window.addEventListener('keydown', handler);
        return () => window.removeEventListener('keydown', handler);
    }, []);

    // Render sidebar content based on active tab
    const renderSidebarContent = () => {
        switch (sidebarTab) {
            case 'workspace':
                return (
                    <div className="p-3">
                        <h3 className="text-xs font-semibold mb-3 text-muted-foreground uppercase">Notebooks</h3>
                        <div className="relative mb-3">
                            <Search className="absolute left-2 top-1/2 -translate-y-1/2 size-3 text-muted-foreground" />
                            <Input
                                placeholder="Search..."
                                value={searchQuery}
                                onChange={(e) => setSearchQuery(e.target.value)}
                                className="h-7 pl-7 text-xs"
                            />
                        </div>
                        {notebooksLoading ? (
                            <p className="text-xs text-muted-foreground">Loading...</p>
                        ) : (
                            <>
                                {allNotebooks
                                    .filter(n => n.name.toLowerCase().includes(searchQuery.toLowerCase()))
                                    .map(nb => (
                                        <div
                                            key={nb.id}
                                            className={`flex items-center gap-2 py-1.5 px-2 rounded text-xs group/notebook ${nb.id === notebookId ? 'bg-primary/10 text-primary' : 'hover:bg-muted'
                                                }`}
                                        >
                                            <span
                                                className="truncate cursor-pointer flex-1"
                                                onClick={() => navigate(`/notebooks/${nb.id}`)}
                                            >
                                                {nb.name}
                                            </span>
                                            <LanguageIcon language={nb.language} />
                                            <DropdownMenu>
                                                <DropdownMenuTrigger asChild>
                                                    <Button
                                                        variant="ghost"
                                                        size="icon"
                                                        className="h-5 w-5 opacity-0 group-hover/notebook:opacity-100 transition-opacity"
                                                        onClick={(e) => e.stopPropagation()}
                                                    >
                                                        <MoreVertical className="h-3 w-3" />
                                                    </Button>
                                                </DropdownMenuTrigger>
                                                <DropdownMenuContent align="end" className="w-40">
                                                    <DropdownMenuItem
                                                        onClick={(e) => {
                                                            e.stopPropagation();
                                                            setRenameTarget({ id: nb.id, name: nb.name });
                                                            setRenameValue(nb.name);
                                                        }}
                                                    >
                                                        <Pencil className="h-3 w-3 mr-2" />
                                                        Rename
                                                    </DropdownMenuItem>
                                                    <DropdownMenuItem
                                                        onClick={(e) => {
                                                            e.stopPropagation();
                                                            handleDeleteNotebook(nb.id, nb.name);
                                                        }}
                                                        className="text-destructive focus:text-destructive"
                                                    >
                                                        <Trash2 className="h-3 w-3 mr-2" />
                                                        Delete
                                                    </DropdownMenuItem>
                                                </DropdownMenuContent>
                                            </DropdownMenu>
                                        </div>
                                    ))
                                }
                                <DropdownMenu>
                                    <DropdownMenuTrigger asChild>
                                        <Button variant="ghost" size="sm" className="w-full mt-2 text-xs">
                                            <Plus className="size-3 mr-1" /> New Notebook
                                        </Button>
                                    </DropdownMenuTrigger>
                                    <DropdownMenuContent>
                                        <DropdownMenuItem onClick={() => openCreateDialog('python')}>
                                            Python Notebook
                                        </DropdownMenuItem>
                                        <DropdownMenuItem onClick={() => openCreateDialog('scala')}>
                                            Scala Notebook
                                        </DropdownMenuItem>
                                    </DropdownMenuContent>
                                </DropdownMenu>
                            </>
                        )}
                    </div >
                );

            case 'toc':
                return (
                    <div className="p-3">
                        <h3 className="text-xs font-semibold mb-3 text-muted-foreground uppercase">Table of Contents</h3>
                        {notebook?.cells.map((cell, index) => (
                            <div key={cell.id} className="flex items-center gap-2 py-1 px-2 rounded hover:bg-muted cursor-pointer text-xs">
                                <span className="text-muted-foreground">Cmd {index + 1}</span>
                                <span className="truncate">{cell.type.toLowerCase() === 'code' ? 'Code' : cell.source.split('\n')[0].replace('#', '').trim()}</span>
                            </div>
                        ))}
                        {(!notebook || notebook.cells.length === 0) && (
                            <p className="text-xs text-muted-foreground">No cells yet</p>
                        )}
                    </div>
                );

            case 'settings':
                return (
                    <div className="p-3">
                        <h3 className="text-xs font-semibold mb-4 text-muted-foreground uppercase">Notebook Settings</h3>
                        <div className="space-y-5">
                            <div>
                                <label className="text-xs text-muted-foreground mb-2 block">Language</label>
                                <Badge variant="outline" className="px-2 py-1 text-xs flex items-center gap-1.5 w-fit">
                                    <img
                                        src={notebook?.language.toUpperCase() === 'PYTHON' ? '/icons/languages/python.png' : '/icons/languages/scala.png'}
                                        alt={notebook?.language}
                                        className="w-3 h-3"
                                    />
                                    {notebook?.language.toUpperCase() === 'PYTHON' ? 'Python' : 'Scala'}
                                </Badge>
                                <p className="text-xs text-muted-foreground mt-1">Language cannot be changed after notebook creation</p>
                            </div>
                            <div>
                                <label className="text-xs text-muted-foreground mb-2 block">Notebook Name</label>
                                <Input
                                    value={notebook?.name || ''}
                                    onChange={(e) => updateNotebook({ name: e.target.value })}
                                    className="h-8 text-xs"
                                />
                            </div>
                            <div className="pt-2">
                                <div className="text-xs">
                                    <p className="mb-2 text-muted-foreground">Kernel Status:</p>
                                    <ConnectionStatusBadge status={connectionStatus} deadReason={deadReason} sparkInitializing={sparkInitPending || runningCells.has('init-spark-context')} />

                                    {/* Pod spawn / terminate progress (k8s_per_user mode).
                                        Shown for non-ready phases so user knows the pod is doing
                                        something rather than seeing a silent spinner. */}
                                    {podPhase && podPhase !== 'ready' && (
                                        <div className={`mt-2 px-2 py-1.5 rounded text-xs flex items-start gap-1.5 ${
                                            podPhase === 'failed'
                                                ? 'bg-red-50 dark:bg-red-950 text-red-700 dark:text-red-300'
                                                : podPhase === 'terminating'
                                                    ? 'bg-amber-50 dark:bg-amber-950 text-amber-700 dark:text-amber-300'
                                                    : 'bg-blue-50 dark:bg-blue-950 text-blue-700 dark:text-blue-300'
                                        }`}>
                                            {podPhase !== 'failed' && <Loader2 className="h-3 w-3 mt-0.5 animate-spin flex-shrink-0" />}
                                            <span>{podMessage || podPhase}</span>
                                        </div>
                                    )}

                                    {connectionStatus === 'connected' ? (
                                        <Button
                                            size="sm"
                                            variant="outline"
                                            className="mt-2 w-full border-red-500 text-red-600 hover:bg-red-50 hover:text-red-700"
                                            onClick={handleDisconnect}
                                        >
                                            Disconnect Kernel
                                        </Button>
                                    ) : connectionStatus === 'connecting' ? (
                                        <Button size="sm" variant="outline" className="mt-2 w-full" disabled>
                                            <Loader2 className="h-3 w-3 mr-1 animate-spin" />
                                            Connecting...
                                        </Button>
                                    ) : (
                                        <Button
                                            size="sm"
                                            className="mt-2 w-full bg-blue-600 hover:bg-blue-700 text-white"
                                            // Block Connect while pod is terminating to avoid the 30-60s wait race.
                                            disabled={podPhase === 'terminating'}
                                            onClick={handleConnect}
                                        >
                                            {podPhase === 'terminating' ? 'Waiting for shutdown…' : 'Connect to Kernel'}
                                        </Button>
                                    )}
                                </div>
                            </div>
                        </div>
                    </div>
                );

            case 'files':
                return <SidebarFiles />;
        }
    };

    // Convert cells for rendering. Memoized so unrelated state changes
    // (toolbar, dialogs, kernel status) don't rebuild N cell objects —
    // CellEditor's memo compares fields, but skipping the map() itself
    // keeps large-notebook renders cheap. Must live above the early
    // returns below (hooks are unconditional).
    const cells: NotebookCell[] = useMemo(() => (notebook?.cells || []).map(c => ({
        id: c.id,
        type: (c.type || 'code').toLowerCase() as CellType,
        source: c.source,
        order: c.order,
        output: c.last_output?.output as CellOutput[] | undefined,
        // Prefer the live in-session duration (captured by useJupyterKernel on
        // execute_reply) over the persisted last_execution_time_ms, so the
        // badge updates immediately when the cell finishes — no DB round-trip.
        executionTime: executionTimes[c.id] ?? c.last_execution_time_ms,
        last_output: c.last_output as { outputs?: CellOutput[]; executed?: boolean } | undefined,
        _frontendId: (c as any)._frontendId, // Pass through stable ID
    })), [notebook?.cells, executionTimes]);

    // Loading state - wait for notebook data
    const isInitialLoad = loading && !notebook;

    if (isInitialLoad || (!notebookId && notebooksLoading)) {
        return (
            <div className="flex items-center justify-center h-screen">
                <Loader2 className="h-8 w-8 animate-spin" />
            </div>
        );
    }

    // No notebooks exist - show welcome/empty screen
    if (!notebookId && !notebooksLoading && allNotebooks.length === 0) {
        return (
            <div className="flex flex-col items-center justify-center h-screen gap-6 bg-background">
                <div className="text-center">
                    <h1 className="text-2xl font-bold mb-2">Welcome to Notebooks</h1>
                    <p className="text-muted-foreground">Create your first notebook to get started</p>
                </div>
                <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                        <Button size="lg">
                            <Plus className="h-5 w-5 mr-2" /> Create Notebook
                        </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent>
                        <DropdownMenuItem onClick={() => openCreateDialog('python')}>
                            Python Notebook
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => openCreateDialog('scala')}>
                            Scala Notebook
                        </DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>
            </div>
        );
    }

    // Error state or notebook not found
    if (error || !notebook) {
        return (
            <div className="flex flex-col items-center justify-center h-screen gap-4">
                <p className="text-red-500">{error || 'Notebook not found'}</p>
                <Button onClick={() => navigate('/notebooks')}>Back to Notebooks</Button>
            </div>
        );
    }

    return (
        <div className="flex h-[calc(100vh-4rem)] w-full overflow-hidden">
            {/* Left Icon Sidebar */}
            <SidebarIconRail
                sidebarTab={sidebarTab}
                sidebarOpen={sidebarOpen}
                onPick={(tab: SidebarTab) => { setSidebarTab(tab); setSidebarOpen(true); }}
            />


            {/* Sidebar Panel */}
            {sidebarOpen && (
                <div className="w-56 border-r border-border bg-card flex flex-col shrink-0">
                    <div className="flex items-center justify-between px-3 py-2 border-b border-border h-12">
                        <span className="text-xs font-semibold capitalize">{sidebarTab}</span>
                        <X className="size-4 cursor-pointer text-muted-foreground hover:text-foreground" onClick={() => setSidebarOpen(false)} />
                    </div>
                    <div className="flex-1 overflow-auto">
                        {renderSidebarContent()}
                    </div>
                </div>
            )}

            {/* Main Content */}
            <main className="flex-1 flex flex-col min-w-0 overflow-hidden">
                {/* Toolbar */}
                <div className="flex items-center justify-between px-2 py-2 border-b border-border bg-background shrink-0 h-12 gap-2 overflow-hidden">
                    <div className="flex items-center gap-2 min-w-0 flex-shrink">

                        {/* Notebook name (read-only) — fixed size so toolbar
                            layout doesn't reflow when switching between
                            notebooks with different name lengths. */}
                        <Input
                            value={notebook.name}
                            readOnly
                            title={notebook.name}
                            className={`font-medium text-sm cursor-default shrink-0 ${compactToolbar ? 'w-32' : 'w-56'}`}
                        />

                        {/* Language display (read-only). Fixed text width so
                            switching between Python/Scala notebooks doesn't
                            shift the toolbar items to the right of this badge. */}
                        <Badge variant="outline" className="px-2 py-1 text-sm font-medium flex items-center gap-1.5" title={notebook.language}>
                            <img
                                src={notebook.language.toUpperCase() === 'PYTHON' ? '/icons/languages/python.png' : '/icons/languages/scala.png'}
                                alt={notebook.language}
                                className="w-4 h-4"
                            />
                            {!compactToolbar && (
                                <span className="inline-block w-12 text-center">
                                    {notebook.language.toUpperCase() === 'PYTHON' ? 'Python' : 'Scala'}
                                </span>
                            )}
                        </Badge>

                        {/* Connection status */}
                        <div
                            role={(sparkInitPending || sparkInitOutputs.length > 0) ? 'button' : undefined}
                            tabIndex={(sparkInitPending || sparkInitOutputs.length > 0) ? 0 : undefined}
                            className={(sparkInitPending || sparkInitOutputs.length > 0) ? 'cursor-pointer' : undefined}
                            title={(sparkInitPending || sparkInitOutputs.length > 0) ? 'Click to view Spark init logs' : undefined}
                            onClick={() => {
                                if (sparkInitPending || sparkInitOutputs.length > 0) setSparkInitLogsOpen(true);
                            }}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    if (sparkInitPending || sparkInitOutputs.length > 0) setSparkInitLogsOpen(true);
                                }
                            }}
                        >
                            <ConnectionStatusBadge status={connectionStatus} deadReason={deadReason} compact={compactToolbar} sparkInitializing={sparkInitPending || runningCells.has('init-spark-context')} />
                        </div>
                    </div>


                    <div className="flex items-center gap-2">
                        {/* Libraries button */}
                        {connectionStatus === 'connected' && (
                            <Button
                                variant="ghost"
                                size="sm"
                                className={`${toolbarBtnBase} ${toolbarBtnCompact}`}
                                onClick={() => {
                                    setLibraryInput(normalizePackageInput((notebook as any)?.cluster_config?.['spark.jars.packages']));
                                    setLibraryDialogOpen(true);
                                }}
                                title="Manage Libraries"
                            >
                                <Package className="size-3" />
                                {!compactToolbar && <span>Libraries</span>}
                            </Button>
                        )}
                        {/* Auto-save status */}
                        <button
                            onClick={handleSave}
                            title={saveStatus === 'saving' ? 'Saving...' : saveStatus === 'unsaved' ? 'Unsaved — click or Ctrl+S to save' : 'All changes saved'}
                            className="p-1.5 rounded hover:bg-muted transition-colors"
                        >
                            {saveStatus === 'saving' ? (
                                <Cloud className="size-4 text-blue-500 animate-pulse" />
                            ) : saveStatus === 'unsaved' ? (
                                <CloudOff className="size-4 text-amber-500" />
                            ) : (
                                <Cloud className="size-4 text-emerald-500" />
                            )}
                        </button>

                        {(() => {
                            const isExecuting = runningCells.size > 0 || pendingCells.size > 0;
                            return (
                                <Button
                                    size="sm"
                                    // "Run All" and "Stop All" are both 7-char, so the
                                    // natural width is already nearly identical — a small
                                    // min-width prevents the 2-3px jitter without padding
                                    // the button out.
                                    style={{ minWidth: compactToolbar ? undefined : 82 }}
                                    className={`${toolbarBtnBase} ${toolbarBtnCompact} ${
                                        isExecuting
                                            ? 'bg-red-600 hover:bg-red-700 text-white border-0'
                                            : 'bg-green-600 hover:bg-green-700 text-white border-0'
                                    }`}
                                    onClick={() => {
                                        if (isExecuting) {
                                            handleInterrupt();
                                        } else {
                                            const sortedCells = [...cells]
                                                .sort((a, b) => a.order - b.order)
                                                .map(c => ({ id: c.id, code: c.source, type: c.type }));
                                            executeAllCells(sortedCells);
                                        }
                                    }}
                                    disabled={connectionStatus !== 'connected'}
                                    title={isExecuting ? 'Stop execution' : 'Run All Cells'}
                                >
                                    {isExecuting ? (
                                        <Square className="size-3 fill-current" />
                                    ) : (
                                        <Play className="size-3 fill-current" />
                                    )}
                                    {!compactToolbar && <span>{isExecuting ? 'Stop All' : 'Run All'}</span>}
                                </Button>
                            );
                        })()}

                        {/* Clear All Outputs button */}
                        <Button
                            variant="outline"
                            size="sm"
                            className={`${toolbarBtnBase} ${toolbarBtnCompact}`}
                            onClick={async () => {
                                for (const cell of cells) {
                                    if (cell.type === 'code') {
                                        await handleClearOutput(cell.id);
                                    }
                                }
                            }}
                            title="Clear All Outputs"
                        >
                            <Eraser className="size-3" />
                            {!compactToolbar && <span>Clear Output</span>}
                        </Button>

                        {/* Connect/Disconnect button — fixed minWidth so all
                            four states (Connect / Disconnect / Connecting… /
                            Shutting down…) take the same horizontal space. */}
                        {connectionStatus === 'connected' ? (
                            <Button
                                variant="outline"
                                size="sm"
                                onClick={handleDisconnect}
                                style={{ minWidth: compactToolbar ? undefined : 105 }}
                                className={`${toolbarBtnBase} ${toolbarBtnCompact} border-red-500 text-red-600 hover:bg-red-50 hover:text-red-700`}
                                title="Disconnect from kernel"
                            >
                                <X className="size-3" />
                                {!compactToolbar && <span>Disconnect</span>}
                            </Button>
                        ) : connectionStatus === 'connecting' ? (
                            <Button
                                variant="outline"
                                size="sm"
                                disabled
                                title="Connecting..."
                                style={{ minWidth: compactToolbar ? undefined : 105 }}
                                className={`${toolbarBtnBase} ${toolbarBtnCompact}`}
                            >
                                <Loader2 className="size-3 animate-spin" />
                                {!compactToolbar && <span>Connecting...</span>}
                            </Button>
                        ) : podPhase === 'terminating' ? (
                            <Button
                                variant="outline"
                                size="sm"
                                disabled
                                style={{ minWidth: compactToolbar ? undefined : 105 }}
                                className={`${toolbarBtnBase} ${toolbarBtnCompact} border-amber-500 text-amber-600`}
                                title={podMessage || 'Waiting for previous pod to shut down…'}
                            >
                                <Loader2 className="size-3 animate-spin" />
                                {!compactToolbar && <span>Shutting down…</span>}
                            </Button>
                        ) : (
                            <Button
                                variant="outline"
                                size="sm"
                                onClick={handleConnect}
                                style={{ minWidth: compactToolbar ? undefined : 105 }}
                                className={`${toolbarBtnBase} ${toolbarBtnCompact} border-blue-500 text-blue-600 hover:bg-blue-50 hover:text-blue-700`}
                                title="Connect to kernel"
                            >
                                <Power className="size-3" />
                                {!compactToolbar && <span>Connect</span>}
                            </Button>
                        )}

                    </div>
                </div>

                {/* Menu Bar Row */}
                <div className="flex items-center px-2 py-1 border-b border-border bg-muted/50 shrink-0 h-8 gap-1">
                    <MenuBar
                        onExportHTML={async () => {
                            try {
                                await exportNotebookAsHTML(notebook.id, notebook.name);
                            } catch (error) {
                                console.error('Export failed:', error);
                            }
                        }}
                        onImportNotebook={async (file) => {
                            try {
                                const newNotebook = await importNotebook(file);
                                if (newNotebook) {
                                    toast.success('Notebook imported');

                                    // Notify list pages to refresh
                                    window.dispatchEvent(new CustomEvent('notebook-list-updated'));

                                    navigate(`/notebooks/${newNotebook.id}`);
                                }
                            } catch (error) {
                                console.error('Import failed:', error);
                            }
                        }}
                        isNotebookPage={true}
                    />
                </div>


                {/* Cells */}
                <div className="flex-1 overflow-auto p-4">
                    {cells.length === 0 ? (
                        <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
                            <p className="mb-4">No cells yet. Add your first cell:</p>
                            <div className="flex gap-2">
                                <Button onClick={() => handleAddCell('code')}>
                                    <Plus className="h-4 w-4 mr-2" /> Code
                                </Button>
                                <Button variant="outline" onClick={() => handleAddCell('markdown')}>
                                    <Plus className="h-4 w-4 mr-2" /> Markdown
                                </Button>
                            </div>
                        </div>
                    ) : (
                        <>
                            {/* Insert at beginning */}
                            <div className="flex justify-center items-center gap-1.5 h-8 opacity-0 hover:opacity-100 transition-opacity" style={{ order: -1 }}>
                                <Button variant="ghost" size="sm" className="h-6 text-xs px-2" onClick={() => handleAddCell('code', -1)}>
                                    <Plus className="h-3 w-3 mr-1" /> Code
                                </Button>
                                <Button variant="ghost" size="sm" className="h-6 text-xs px-2" onClick={() => handleAddCell('markdown', -1)}>
                                    <Plus className="h-3 w-3 mr-1" /> Markdown
                                </Button>
                            </div>

                            {/* Cells container with flexbox - use CSS order instead of sorting */}
                            <div className="flex flex-col">
                                {cells
                                    .filter(cell => !(cell.source || '').includes('__SPARK_INIT__'))
                                    .map((cell) => (
                                        <div key={cell._frontendId || cell.id} data-cell-id={cell.id} style={{ order: cell.order }}>
                                            <CellEditor
                                                cell={cell}
                                                isRunning={runningCells.has(cell.id)}
                                                executionStartedAtMs={runningCellStarts[cell.id]}
                                                isPending={pendingCells.has(cell.id)}
                                                kernelBusy={sparkInitPending || runningCells.has('init-spark-context')}
                                                // Raw kernel exec_count — same as standard Jupyter. May start at
                                                // a number > 1 because hidden init cells consumed earlier counters.
                                                // Monotonic, never negative, matches kernel state exactly.
                                                executionCount={executionCounts[cell.id]}
                                                hasExecuted={executedCells.has(cell.id) || !!cell.last_output?.executed || (!!cellOutputs[cell.id] && cellOutputs[cell.id].length > 0)}
                                                output={cellOutputs[cell.id]}
                                                language={notebook.language.toLowerCase()}
                                                onUpdate={(source) => handleUpdateCell(cell.id, source)}
                                                onRun={(sourceOverride?: string) => handleRunCell(cell.id, sourceOverride ?? cell.source)}
                                                onInterrupt={handleInterrupt}
                                                onClearOutput={() => handleClearOutput(cell.id)}
                                                onDelete={() => handleDeleteCell(cell.id)}
                                                onMoveUp={() => handleMoveCell(cell.id, 'up')}
                                                onMoveDown={() => handleMoveCell(cell.id, 'down')}
                                            />
                                            {/* Insert after this cell */}
                                            <div className="flex justify-center items-center gap-1.5 h-8 opacity-0 hover:opacity-100 transition-opacity">
                                                <Button variant="ghost" size="sm" className="h-6 text-xs px-2" onClick={() => handleAddCell('code', cell.order)}>
                                                    <Plus className="h-3 w-3 mr-1" /> Code
                                                </Button>
                                                <Button variant="ghost" size="sm" className="h-6 text-xs px-2" onClick={() => handleAddCell('markdown', cell.order)}>
                                                    <Plus className="h-3 w-3 mr-1" /> Markdown
                                                </Button>
                                            </div>
                                        </div>
                                    ))}
                            </div>
                        </>
                    )}
                </div>
            </main>

            {/* Kernel Connection Dialog */}
            <KernelConnectionDialog
                open={kernelDialogOpen}
                onClose={() => setKernelDialogOpen(false)}
                language={notebookLanguage}
                onConnect={handleKernelDialogConnect}
                savedPackages={(notebook as any)?.cluster_config?.['spark.jars.packages'] || ''}
                savedIcebergWarehousePath={(notebook as any)?.cluster_config?.['spark.sql.catalog.iceberg.warehouse'] || ''}
            />

            {/* Create Notebook Dialog */}
            <Dialog open={createDialogOpen} onOpenChange={setCreateDialogOpen}>
                <DialogContent className="sm:max-w-md">
                    <DialogHeader>
                        <DialogTitle>Create {createLanguage === 'scala' ? 'Scala' : 'Python'} Notebook</DialogTitle>
                    </DialogHeader>
                    <div className="space-y-3 py-2">
                        <div className="space-y-2">
                            <Label htmlFor="notebook-name">Notebook Name</Label>
                            <Input
                                id="notebook-name"
                                value={createName}
                                onChange={e => { setCreateName(e.target.value); setCreateError(''); }}
                                onKeyDown={e => { if (e.key === 'Enter') confirmCreateNotebook(); }}
                                autoFocus
                            />
                            {createError && <p className="text-xs text-destructive">{createError}</p>}
                        </div>
                    </div>
                    <DialogFooter>
                        <Button variant="outline" onClick={() => setCreateDialogOpen(false)}>Cancel</Button>
                        <Button onClick={confirmCreateNotebook} disabled={!createName.trim()}>Create</Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>

            {/* Delete Confirmation Dialog */}
            <AlertDialog open={deleteConfirmOpen} onOpenChange={setDeleteConfirmOpen}>
                <AlertDialogContent>
                    <AlertDialogHeader>
                        <AlertDialogTitle>Delete Notebook</AlertDialogTitle>
                        <AlertDialogDescription>
                            Are you sure you want to delete "{notebookToDelete?.name}"? This action cannot be undone.
                        </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                        <AlertDialogCancel onClick={() => {
                            setDeleteConfirmOpen(false);
                            setNotebookToDelete(null);
                        }}>
                            Cancel
                        </AlertDialogCancel>
                        <AlertDialogAction onClick={confirmDeleteNotebook} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">
                            Delete
                        </AlertDialogAction>
                    </AlertDialogFooter>
                </AlertDialogContent>
            </AlertDialog>

            {/* Rename Notebook Dialog */}
            <Dialog open={!!renameTarget} onOpenChange={(open) => { if (!open) setRenameTarget(null); }}>
                <DialogContent className="max-w-sm">
                    <DialogHeader>
                        <DialogTitle>Rename Notebook</DialogTitle>
                    </DialogHeader>
                    <Input
                        value={renameValue}
                        onChange={(e) => setRenameValue(e.target.value)}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter' && renameValue.trim() && renameTarget) {
                                import('@/services/notebookService').then(m => m.notebookService.updateNotebook(renameTarget.id, { name: renameValue.trim() })).then(() => { loadNotebooks(); if (renameTarget.id === notebookId) { setNotebook(prev => prev ? { ...prev, name: renameValue.trim() } : null); } setRenameTarget(null); });
                            }
                        }}
                        autoFocus
                    />
                    <DialogFooter>
                        <Button variant="outline" onClick={() => setRenameTarget(null)}>Cancel</Button>
                        <Button onClick={() => {
                            if (renameValue.trim() && renameTarget) {
                                import('@/services/notebookService').then(m => m.notebookService.updateNotebook(renameTarget.id, { name: renameValue.trim() })).then(() => { loadNotebooks(); if (renameTarget.id === notebookId) { setNotebook(prev => prev ? { ...prev, name: renameValue.trim() } : null); } setRenameTarget(null); });
                            }
                        }} disabled={!renameValue.trim()}>Rename</Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>

            {/* Spark Init Logs Dialog (system cell output, even if hidden) */}
            <Dialog open={sparkInitLogsOpen} onOpenChange={setSparkInitLogsOpen}>
                <DialogContent className="max-w-2xl">
                    <DialogHeader>
                        <DialogTitle>Spark Init Logs</DialogTitle>
                    </DialogHeader>
                    <div className="space-y-2">
                        <p className="text-xs text-muted-foreground">
                            Output from system cell <code>init-spark-context</code>.
                        </p>
                        <textarea
                            className="min-h-[320px] w-full rounded-md border border-input bg-background px-3 py-2 text-xs font-mono text-foreground"
                            value={sparkInitLogText || '(no output yet)'}
                            readOnly
                        />
                    </div>
                    <DialogFooter>
                        <Button variant="outline" onClick={() => setSparkInitLogsOpen(false)}>Close</Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>

            {/* Libraries Dialog */}
            <Dialog open={libraryDialogOpen} onOpenChange={setLibraryDialogOpen}>
                <DialogContent className="max-w-md">
                    <DialogHeader>
                        <DialogTitle className="flex items-center gap-2">
                            <Package className="size-5" /> Manage Libraries
                        </DialogTitle>
                    </DialogHeader>
                    <div className="space-y-3">
                        <p className="text-xs text-muted-foreground">
                            Add Maven coordinates. Kernel will restart to apply changes.
                        </p>
                        <div className="space-y-1.5">
                            {libraryRows.map((pkg, index) => (
                                <div key={`library-package-${index}`} className="flex items-center gap-2">
                                    <input
                                        className="flex h-9 w-full rounded-md border border-input bg-background px-2 py-1 text-xs font-mono placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                                        placeholder={notebook?.language === 'scala'
                                            ? 'io.delta:delta-core_2.12:2.4.0'
                                            : 'org.apache.hadoop:hadoop-aws:3.3.4'}
                                        value={pkg}
                                        onChange={e => {
                                            const nextRows = [...libraryRows];
                                            nextRows[index] = e.target.value;
                                            setLibraryInput(normalizePackageInput(nextRows.join('\n')));
                                        }}
                                    />
                                    {pkg.trim() && (
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="icon"
                                            className="h-8 w-8 shrink-0 text-muted-foreground"
                                            onClick={() => {
                                                const nextRows = libraryRows.filter((_, rowIndex) => rowIndex !== index);
                                                setLibraryInput(normalizePackageInput(nextRows.join('\n')));
                                            }}
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
                                onClick={() => setLibraryInput(libraryRows.filter(Boolean).concat('').join('\n'))}
                            >
                                <Plus className="mr-1 h-3.5 w-3.5" />
                                Add package
                            </Button>
                        </div>
                        {packageListFromInput(libraryInput).length > 0 && (
                            <div className="text-[10px] text-muted-foreground">
                                {packageListFromInput(libraryInput).length} {packageListFromInput(libraryInput).length === 1 ? 'package' : 'packages'}
                            </div>
                        )}
                    </div>
                    <DialogFooter>
                        <Button variant="outline" onClick={() => setLibraryDialogOpen(false)}>Cancel</Button>
                        <Button variant="destructive" onClick={async () => {
                            setLibraryDialogOpen(false);
                            const packages = packageListFromInput(libraryInput).join(',') || undefined;
                            try {
                                toast.info('Restarting kernel with new libraries...');
                                // Persist packages to notebook config so they survive reload.
                                try {
                                    const cfg = { ...((notebook as any)?.cluster_config || {}) };
                                    if (packages) cfg['spark.jars.packages'] = packages;
                                    else delete cfg['spark.jars.packages'];
                                    await updateNotebook({ cluster_config: cfg });
                                } catch (e) {
                                    console.warn('[NotebookPage] Failed to persist package list:', e);
                                }
                                // Push into state so the auto-init effect rebuilds init code
                                // with the new package list once kernel comes back idle.
                                setSparkPackages(packages);
                                // In-pod restart — no pod respawn. New SparkSession is built by
                                // the auto-injected init cell (which reads sparkPackages state)
                                // when the kernel reports 'connected' again (~1-2s).
                                await restart();
                            } catch { toast.error('Failed to update libraries'); }
                        }}>
                            Apply & Restart Kernel
                        </Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>

            {/* Disconnect Confirmation Dialog */}
            <AlertDialog open={disconnectConfirmOpen} onOpenChange={setDisconnectConfirmOpen}>
                <AlertDialogContent>
                    <AlertDialogHeader>
                        <AlertDialogTitle>Disconnect Kernel?</AlertDialogTitle>
                        <AlertDialogDescription>
                            <span className="font-semibold text-amber-600">Warning:</span> All unsaved in-memory variables will be lost.
                            <br /><br />
                            <strong>Disconnect</strong> — close connection, keep kernel alive.
                            <br />
                            <strong>Restart</strong> — clear variables, keep pod & libraries (~2s).
                            <br />
                            <strong>Shutdown</strong> — kill kernel & destroy pod (~30s to free).
                        </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90" onClick={async () => {
                            flushSync(() => {
                                setDisconnectConfirmOpen(false);
                                markDisconnecting();
                            });
                            try {
                                await axios.delete(`/api/v1/notebooks/${notebookId}/kernel/shutdown`);
                                // Track pod terminate in BG so sidebar shows "Shutting down..." until pod is gone.
                                void trackPodStatus('empty');
                                await disconnect();
                                toast.success('Kernel shutdown');
                            } catch { toast.error('Shutdown failed'); }
                        }}>
                            Shutdown
                        </AlertDialogAction>
                        <AlertDialogAction className="bg-blue-500 text-white hover:bg-blue-600" onClick={async () => {
                            setDisconnectConfirmOpen(false);
                            try {
                                toast.info('Restarting kernel...');
                                // In-pod restart — kernel process dies & respawns in the same
                                // pod (~1-2s). No pod destroy, no full reconnect, no Spark
                                // package change. For library changes use the dedicated dialog.
                                await restart();
                            } catch { toast.error('Restart failed'); }
                        }}>
                            Restart
                        </AlertDialogAction>
                        <AlertDialogAction className="bg-amber-500 text-white hover:bg-amber-600" onClick={confirmDisconnect}>
                            Disconnect
                        </AlertDialogAction>
                    </AlertDialogFooter>
                </AlertDialogContent>
            </AlertDialog>


        </div>
    );
}
