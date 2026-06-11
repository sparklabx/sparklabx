// Deep ESM entries of monaco-editor ship no adjacent .d.ts. edcore.main
// re-exports the same API surface as editor.api (which is typed), so alias
// the types; the language contributions are side-effect-only modules.
declare module 'monaco-editor/esm/vs/editor/edcore.main.js' {
    export * from 'monaco-editor/esm/vs/editor/editor.api';
}

declare module 'monaco-editor/esm/vs/basic-languages/*';
