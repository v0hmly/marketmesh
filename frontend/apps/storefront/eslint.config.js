import tseslint from 'typescript-eslint';
import vue from 'eslint-plugin-vue';
import { fileURLToPath } from 'node:url';
import { applicationConfig } from '../../tools/architecture/eslint.mjs';
export default applicationConfig(
  tseslint,
  vue,
  fileURLToPath(new URL('./src/', import.meta.url)),
  'storefront',
);
