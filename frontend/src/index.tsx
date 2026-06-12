import React from 'react';
import ReactDOM from 'react-dom/client';
import './index.css';
import App from './App';

// Monaco is configured in components/Notebooks/monaco/setup.ts (imported by
// the lazy NotebookPage chunk) so the editor bundle is not downloaded on the
// login screen or notebook list.

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
