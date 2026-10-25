import js from '@eslint/js';
import { defineConfig } from 'eslint/config';
import sonarjs from 'eslint-plugin-sonarjs';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';

const testFiles = ['src/**/*.test.ts', 'e2e/**/*.test.ts'];

export default defineConfig(
  {
    ignores: [
      '.svelte-kit/',
      '.stryker-tmp/',
      'coverage/',
      'dist/',
      'playwright-report/',
      'reports/',
      'test-results/',
      'src/lib/api.gen.d.ts',
    ],
  },
  js.configs.recommended,
  ts.configs.strictTypeChecked,
  ts.configs.stylisticTypeChecked,
  svelte.configs.recommended,
  {
    languageOptions: {
      globals: globals.browser,
      parserOptions: {
        projectService: true,
        extraFileExtensions: ['.svelte'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  {
    files: ['*.js', '*.ts', 'e2e/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
  // The Svelte parser also reads a rune module (*.svelte.ts), so both need the TypeScript parser under it.
  {
    files: ['**/*.svelte', '**/*.svelte.ts'],
    languageOptions: { parserOptions: { parser: ts.parser } },
  },
  {
    plugins: { sonarjs },
    rules: {
      complexity: ['error', 15],
      'max-depth': ['error', 4],
      'max-lines-per-function': ['error', { max: 60, skipBlankLines: true, skipComments: true }],
      'max-statements': ['error', 40],
      'sonarjs/cognitive-complexity': ['error', 20],
    },
  },
  // The Go tree exempts its test files from the complexity linters too.
  {
    files: testFiles,
    rules: {
      complexity: 'off',
      'max-lines-per-function': 'off',
      'max-statements': 'off',
      'sonarjs/cognitive-complexity': 'off',
    },
  },
);
