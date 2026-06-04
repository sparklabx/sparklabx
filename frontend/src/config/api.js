export const getApiBaseUrl = () => {
  const hostname = window.location.hostname;
  const port = window.location.port;

  // Development: Check if running on dev port (3000 or 3001)
  if (hostname === 'localhost' || hostname === '127.0.0.1') {
    console.log('🔧 Development mode - using Vite request proxy');
    return '';
  }

  // Development on lh01 with dev port
  if (hostname === 'lh01' && (port === '3000' || port === '3001')) {
    console.log('🔧 Development mode (lh01) - using Vite request proxy');
    return '';
  }

  // Production: use relative path (nginx proxy)
  // All /api/* calls will be proxied by nginx to backend
  console.log('🔧 Production mode - using relative path (nginx proxy)');
  return ''; // Empty string = relative path /api/*
};

// Fallback URLs for different services
export const getSparkApiUrl = () => {
  // Always use the main API base URL
  return getApiBaseUrl();
};

export const getAirflowApiUrl = () => {
  return getApiBaseUrl(); // Always use FastAPI for Airflow
};

export const API_BASE_URL = getApiBaseUrl();

// Helper function to get auth endpoint based on environment
export const getAuthEndpoint = (path) => {
  const hostname = window.location.hostname;
  const port = window.location.port;

  const isDevMode = (hostname === 'localhost' || hostname === '127.0.0.1') ||
    (hostname === 'lh01' && (port === '3000' || port === '3001'));

  // Remove leading /api/auth if present
  const cleanPath = path.replace(/^\/api\/auth/, '');

  if (isDevMode) {
    return `/auth${cleanPath}`;
  } else {
    return `/api/auth${cleanPath}`;
  }
};

export default getApiBaseUrl;