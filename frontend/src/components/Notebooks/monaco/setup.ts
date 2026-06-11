/**
 * Monaco bootstrap — imported (side-effect) from registerStatic.ts, which
 * NotebookPage pulls in statically. That places Monaco in the lazy
 * NotebookPage chunk: the login screen and notebook list no longer download
 * the editor at all.
 *
 * We deliberately import `edcore.main` (full editor feature set, no
 * language services) plus only the language contributions we ship, instead
 * of the `monaco-editor` root entry which bundles ~80 basic languages and
 * the JSON/CSS/HTML/TypeScript worker services we never use.
 *
 * Local bundle (no CDN) is still required for K8s environments where CDN
 * may be blocked — loader.config({ monaco }) keeps that guarantee.
 */
import * as monaco from 'monaco-editor/esm/vs/editor/edcore.main.js';
import 'monaco-editor/esm/vs/basic-languages/python/python.contribution.js';
import 'monaco-editor/esm/vs/basic-languages/scala/scala.contribution.js';
import 'monaco-editor/esm/vs/basic-languages/sql/sql.contribution.js';
import 'monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution.js';
import { loader } from '@monaco-editor/react';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';

// Real editor worker (tokenization helpers, word-based suggestions, diff)
// instead of the previous no-op Blob worker that forced Monaco to fall back
// to main-thread processing.
(self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
    getWorker() {
        return new EditorWorker();
    },
};

loader.config({ monaco: monaco as unknown as typeof import('monaco-editor') });
