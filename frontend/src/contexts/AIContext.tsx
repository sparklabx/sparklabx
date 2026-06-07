import { createContext, useContext } from 'react';

// No-op context kept for backward compatibility with notebook components
// that were originally written for a sibling AI-panel app. The hook returns
// safe defaults so existing call sites compile without changes.
interface AIContextType {
  aiPanelOpen: boolean;
  setAiPanelOpen: (open: boolean) => void;
  pendingPrompt: string | null;
  sendPrompt: (prompt: string) => void;
  clearPendingPrompt: () => void;
  registerPageContext: (getter: () => unknown) => void;
  unregisterPageContext: () => void;
  getPageContext: () => unknown;
}

export const AIContext = createContext<AIContextType>({
  aiPanelOpen: false, setAiPanelOpen: () => { },
  pendingPrompt: null, sendPrompt: () => { },
  clearPendingPrompt: () => { },
  registerPageContext: () => { }, unregisterPageContext: () => { },
  getPageContext: () => null,
});

export const useAIContext = () => useContext(AIContext);
