/// <reference types="vite/client" />

// Extend Window interface for env-config.js
interface Window {
  _env_?: {
    REACT_APP_API_URL?: string;
    [key: string]: any;
  };
}
