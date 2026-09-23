#!/usr/bin/env node
// Генерирует tokens.css из tokens.json.
// Запуск: node frontend/tools/generate-tokens-css.mjs
//
// Тёмная тема раскладывается в три места, как требует
// frontend/apps/storefront/AGENTS.md: :root[data-theme='dark'] для явного
// выбора и @media (prefers-color-scheme: dark) для системного.

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = join(dirname(fileURLToPath(import.meta.url)), '../../docs/design/design-system');
const tokens = JSON.parse(readFileSync(join(here, 'tokens.json'), 'utf8'));

const lines = (pairs, indent) => pairs.map(([n, v]) => `${indent}--${n}: ${v};`).join('\n');

const light = [];
const dark = [];

for (const t of tokens.color.tokens) {
  light.push([t.name, t.value.light]);
  dark.push([t.name, t.value.dark]);
}
for (const [name, stack] of Object.entries(tokens.type.families)) light.push([`font-${name}`, stack]);
for (const group of tokens.type.groups) {
  for (const s of group.styles) {
    light.push([`text-${s.name}-size`, s.fontSize]);
    light.push([`text-${s.name}-weight`, String(s.fontWeight)]);
    if (s.lineHeight) light.push([`text-${s.name}-leading`, String(s.lineHeight)]);
    if (s.letterSpacing) light.push([`text-${s.name}-tracking`, s.letterSpacing]);
  }
}
for (const t of tokens.spacing.tokens) light.push([t.name, t.value]);
for (const t of tokens.radius.tokens) light.push([t.name, t.value]);
for (const t of tokens.size.tokens) light.push([t.name, t.value]);
for (const t of tokens.shadow.tokens) {
  light.push([t.name, t.value.light]);
  dark.push([t.name, t.value.dark]);
}

const css = `/* Сгенерировано из tokens.json — не редактировать вручную.
   Источник: docs/design/design-system/tokens.json
   Команда:  node frontend/tools/generate-tokens-css.mjs
   Система:  ${tokens.name}, версия токенов ${tokens.version} */

:root {
  color-scheme: light dark;
${lines(light, '  ')}
}

:root[data-theme='dark'] {
${lines(dark, '  ')}
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme]) {
${lines(dark, '    ')}
  }
}
`;

writeFileSync(join(here, 'tokens.css'), css);
process.stdout.write(`tokens.css: ${light.length} переменных в светлой теме, ${dark.length} в тёмной\n`);
