import { loader } from '@monaco-editor/react';
import { registerPythonStatic } from './pythonStaticCompletions';
import { registerScalaStatic } from './scalaStaticCompletions';
import { registerSqlStatic } from './sqlStaticCompletions';

// Module-level flag so the static providers only register once across
// mounts. Monaco's language registry is global, so re-registering would
// duplicate every suggestion.
let isRegistered = false;

export function registerAllStaticProviders() {
    if (isRegistered) return;
    isRegistered = true;
    loader.init().then(monaco => {
        registerPythonStatic(monaco);
        registerScalaStatic(monaco);
        registerSqlStatic(monaco);
    });
}
