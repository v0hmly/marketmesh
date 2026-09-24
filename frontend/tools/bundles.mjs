// Reproducible manifest closures, not just the size of the entry filename.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { gzipSync, brotliCompressSync, constants } from "node:zlib";
const workspace = resolve(import.meta.dirname, "..");
const application = process.argv[2];
if (!["storefront", "staff"].includes(application))
  throw new Error("Choose storefront or staff");
const dist = resolve(workspace, "apps", application, "dist");
const manifest = JSON.parse(readFileSync(resolve(dist, ".vite/manifest.json")));
const entry = Object.keys(manifest).find((key) => manifest[key].isEntry);
if (!entry) throw new Error("Missing entry");
function closure(keys) {
  const files = new Set();
  const seen = new Set();
  function visit(key) {
    if (seen.has(key)) return;
    seen.add(key);
    const chunk = manifest[key];
    if (!chunk) throw new Error(`Missing chunk ${key}`);
    files.add(chunk.file);
    for (const css of chunk.css ?? []) files.add(css);
    for (const dependency of chunk.imports ?? []) visit(dependency);
  }
  keys.forEach(visit);
  return [...files];
}
function size(files) {
  const result = {
    js: { raw: 0, gzip: 0, brotli: 0 },
    css: { raw: 0, gzip: 0, brotli: 0 },
  };
  for (const file of files) {
    const kind = file.endsWith(".js")
      ? "js"
      : file.endsWith(".css")
        ? "css"
        : null;
    if (!kind) continue;
    const bytes = readFileSync(resolve(dist, file));
    result[kind].raw += bytes.length;
    result[kind].gzip += gzipSync(bytes, { level: 9 }).length;
    result[kind].brotli += brotliCompressSync(bytes, {
      params: { [constants.BROTLI_PARAM_QUALITY]: 11 },
    }).length;
  }
  return result;
}
const report = { application, initial: size(closure([entry])), routes: {} };
for (const [key, chunk] of Object.entries(manifest)) {
  if (chunk.isDynamicEntry) {
    const layout = "src/modules/account/views/AccountLayout.vue";
    const parents =
      application === "storefront" && key.startsWith("src/modules/account/")
        ? [layout]
        : [];
    report.routes[key] = size(closure([entry, ...parents, key]));
  }
}
const output = JSON.stringify(report, null, 2);
if (!process.argv.includes("--check")) process.stdout.write(output + "\n");
if (process.argv.includes("--check")) {
  process.stdout.write(
    `${application}: initial JS ${report.initial.js.raw} bytes, CSS ${report.initial.css.raw} bytes; ${Object.keys(report.routes).length} lazy entries\n`,
  );
  const limits = JSON.parse(
    readFileSync(resolve(workspace, "bundle-budgets.json")),
  )[application];
  for (const kind of ["js", "css"]) {
    if (report.initial[kind].raw > limits.initial[kind])
      throw new Error(`${application} initial ${kind} budget exceeded`);
    for (const [route, stats] of Object.entries(report.routes)) {
      if (stats[kind].raw > limits.route[kind])
        throw new Error(`${application} ${route} ${kind} budget exceeded`);
    }
  }
}
