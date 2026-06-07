import React from 'react';
import ReactDOM from 'react-dom/client';
import './index.css';
import App from './App';

// Configure Monaco Editor to use local bundle instead of CDN
// This is required for K8s environments where CDN may be blocked
import { loader } from '@monaco-editor/react';
import * as monaco from 'monaco-editor';

// Configure Monaco environment for workers
(self as any).MonacoEnvironment = {
  getWorker(_workerId: string, _label: string) {
    // Return inline worker for all types
    const workerUrl = URL.createObjectURL(
      new Blob(['self.MonacoEnvironment = { baseUrl: "' + window.location.origin + '" };'], {
        type: 'text/javascript',
      })
    );
    return new Worker(workerUrl, { type: 'module' });
  },
};

loader.config({ monaco });

// Suppress ResizeObserver errors (common with react-resizable-panels)
// This is a known harmless error that doesn't affect functionality

// Patch ResizeObserver to prevent loop errors
if (typeof window !== 'undefined') {
  const OriginalResizeObserver = window.ResizeObserver;

  window.ResizeObserver = class ResizeObserver extends OriginalResizeObserver {
    constructor(callback: ResizeObserverCallback) {
      super((entries, observer) => {
        requestAnimationFrame(() => {
          callback(entries, observer);
        });
      });
    }
  } as any;
}

// Override console.error to suppress ResizeObserver errors
const originalError = console.error;
console.error = (...args: any[]) => {
  if (
    typeof args[0] === 'string' &&
    args[0].includes('ResizeObserver')
  ) {
    return;
  }
  originalError.apply(console, args);
};

// Suppress error events
window.addEventListener('error', (e) => {
  if (
    e.message &&
    (e.message.includes('ResizeObserver loop') ||
      e.message.includes('ResizeObserver'))
  ) {
    e.stopImmediatePropagation();
    e.preventDefault();
  }
}, true);

const root = ReactDOM.createRoot(
  document.getElementById('root') as HTMLElement
);
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
