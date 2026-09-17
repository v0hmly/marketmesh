import tseslint from 'typescript-eslint';
import vue from 'eslint-plugin-vue';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const sourceRoot = fileURLToPath(new URL('./src/', import.meta.url));
const boundaries = {
  meta: {
    type: 'problem',
    schema: [],
    messages: { forbidden: 'Import crosses a product-module boundary.' },
  },
  create(context) {
    const source = path.relative(sourceRoot, context.filename).split(path.sep).join('/');
    const owner = /^modules\/([^/]+)\//.exec(source)?.[1];
    function check(node) {
      const specifier = node.source?.value;
      if (!node.source) return;
      const generated = typeof specifier === 'string' && specifier.startsWith('@marketmesh/api');
      if (
        typeof specifier !== 'string' ||
        specifier.startsWith('/') ||
        specifier.startsWith('file:') ||
        (generated &&
          (!source.startsWith('shared/api/') ||
            node.type === 'ImportExpression' ||
            node.type === 'ExportAllDeclaration'))
      ) {
        context.report({ node, messageId: 'forbidden' });
        return;
      }
      if (typeof specifier !== 'string' || !specifier.startsWith('.')) return;
      const target = path
        .relative(sourceRoot, path.resolve(path.dirname(context.filename), specifier))
        .split(path.sep)
        .join('/');
      const targetOwner = /^modules\/([^/]+)\//.exec(target)?.[1];
      if (
        target.startsWith('../') ||
        (owner && targetOwner && owner !== targetOwner) ||
        (source.startsWith('shared/') && /^(shell|modules)\//.test(target)) ||
        (owner &&
          target.startsWith('shell/') &&
          !/^shell\/(context|session(?:\/index)?)(?:\.ts)?$/.test(target))
      ) {
        context.report({ node, messageId: 'forbidden' });
      }
    }
    return {
      ImportDeclaration: check,
      ExportNamedDeclaration: check,
      ExportAllDeclaration: check,
      ImportExpression: check,
    };
  },
};

export default [
  { ignores: ['dist/**', 'test-results/**', 'playwright-report/**'] },
  ...tseslint.configs.recommended,
  ...vue.configs['flat/essential'],
  {
    files: ['**/*.vue'],
    languageOptions: { parserOptions: { parser: tseslint.parser } },
  },
  {
    files: ['src/**/*.{ts,vue}'],
    plugins: { marketmesh: { rules: { boundaries } } },
    rules: {
      'marketmesh/boundaries': 'error',
      'no-console': 'error',
      'no-eval': 'error',
      'vue/no-v-html': 'error',
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: ['@marketmesh/api/**'],
              message: 'Use the public API boundary in shared/api.',
            },
          ],
        },
      ],
    },
  },
  {
    files: ['src/shared/api/*.ts'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          paths: [
            {
              name: '@marketmesh/api/auth/v1/auth_pb',
              allowImportNames: ['AuthService'],
              message: 'Only the public AuthService is a browser API.',
            },
          ],
          patterns: [
            {
              group: [
                '@marketmesh/api/gateway/**',
                '@marketmesh/api/tunnel/**',
                '@marketmesh/api/marketmesh_test/**',
                '@marketmesh/api/auth/v1/session_pb',
                '@marketmesh/api/auth/v1/events_pb',
              ],
              message: 'Private transport and assertion APIs are not browser APIs.',
            },
          ],
        },
      ],
    },
  },
  // Component integration tests compose the real shell router; production
  // modules still depend only on its public context/session interface.
  { files: ['src/**/*.test.ts'], rules: { 'marketmesh/boundaries': 'off' } },
];
