/**
 * SparkLabX - Brand Colors & Theme Configuration
 * Primary Color: Orange (from logo)
 */

export const sparklabxTheme = {
  // Primary Brand Colors
  primary: {
    main: '#F97316',      // Orange-500 (primary brand color)
    light: '#FB923C',     // Orange-400 (hover states)
    dark: '#EA580C',      // Orange-600 (active states)
    lighter: '#FDBA74',   // Orange-300 (backgrounds)
    lightest: '#FED7AA',  // Orange-200 (subtle backgrounds)
  },

  // Secondary Colors
  secondary: {
    main: '#0EA5E9',      // Sky-500 (complementary blue)
    light: '#38BDF8',     // Sky-400
    dark: '#0284C7',      // Sky-600
  },

  // Status Colors (keep standard)
  status: {
    success: '#52c41a',   // Green
    error: '#f5222d',     // Red
    warning: '#faad14',   // Gold/Yellow
    info: '#0EA5E9',      // Sky blue
  },

  // UI States
  states: {
    running: '#1890ff',   // Blue (Ant Design blue)
    success: '#52c41a',   // Green
    failed: '#f5222d',    // Red
    queued: '#faad14',    // Gold
    paused: '#FB923C',    // Light Orange
  },

  // Neutral Colors
  neutral: {
    text: '#262626',
    textSecondary: '#666666',
    textDisabled: '#8c8c8c',
    border: '#d9d9d9',
    background: '#f5f5f5',
    backgroundLight: '#fafafa',
  },
};

export default sparklabxTheme;
