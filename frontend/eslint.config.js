import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['build', 'node_modules', 'public'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // react-refresh: conflicts with shadcn/cva pattern that exports
      // both a component and its variants helper from the same file.
      // Off across the board since SparkLabX uses shadcn extensively.
      'react-refresh/only-export-components': 'off',
      // no-explicit-any: subjective; codebase has ~80 legit any uses
      // around Monaco/Jupyter kernel messages and 3rd-party untyped
      // libraries. Off to keep CI signal high; revisit per-file later
      // if we want gradual narrowing.
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unused-vars': [
        'warn',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  },
);
