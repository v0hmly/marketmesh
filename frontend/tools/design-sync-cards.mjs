// Карточки Claude Design для дизайн-системы без React-компонентов (MM-141).
// Каждый фрагмент .design-sync/cards/<папка>/<Имя>.html начинается строкой
// <!-- group: <Группа>[; viewport: WxH] --> и содержит разметку на классах styles.css.
// Скрипт оборачивает его в карточку components/<папка>/<Имя>/<Имя>.html бандла.
// Запуск из корня репозитория после resync.mjs:
// node frontend/tools/design-sync-cards.mjs ./ds-bundle
import {
  existsSync,
  rmSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = join(
  dirname(fileURLToPath(import.meta.url)),
  "../../.design-sync/cards",
);
const out = process.argv[2] ?? "ds-bundle";
if (!existsSync(join(out, "styles.css"))) {
  console.error(`${out}/styles.css не найден: сначала соберите бандл`);
  process.exit(1);
}
// Система без React-компонентов: components/ целиком принадлежит карточкам,
// поэтому устаревшие карточки удаляются вместе с каталогом.
rmSync(join(out, "components"), { recursive: true, force: true });
const escape = (value) =>
  value.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
let count = 0;
for (const dir of readdirSync(here, { withFileTypes: true })) {
  if (!dir.isDirectory()) continue;
  for (const file of readdirSync(join(here, dir.name)).filter((f) =>
    f.endsWith(".html"),
  )) {
    const source = readFileSync(join(here, dir.name, file), "utf8").replace(
      /\r\n/g,
      "\n",
    );
    const head = source.match(
      /^<!--\s*group:\s*([^;]+?)\s*(?:;\s*viewport:\s*(\d+x\d+)\s*)?-->\n/,
    );
    if (!head) {
      console.error(
        `${dir.name}/${file}: первая строка должна быть <!-- group: … -->`,
      );
      process.exit(1);
    }
    const name = basename(file, ".html");
    const viewport = head[2] ? ` viewport="${head[2]}"` : "";
    const html = `<!-- @dsCard group="${escape(head[1])}"${viewport} -->
<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escape(name)}</title>
  <link rel="stylesheet" href="../../../styles.css">
  <style>body{margin:0;padding:var(--sp-6);background:var(--surface-page);color:var(--text-primary);font-family:var(--font-sans)}</style>
</head><body>
${source.slice(head[0].length)}</body></html>
`;
    const target = join(out, "components", dir.name, name);
    mkdirSync(target, { recursive: true });
    writeFileSync(join(target, `${name}.html`), html);
    count++;
  }
}
console.log(`cards: ${count}`);
