/**
 * Axios instance for API calls
 * Uses the global axios instance from authService which already has JWT interceptors configured
 */
import axios from 'axios';

// Export the configured axios instance
// Note: authService.js already sets up interceptors for JWT token
// Do NOT set baseURL here - let each service use full URLs for interceptor to work
export default axios;
