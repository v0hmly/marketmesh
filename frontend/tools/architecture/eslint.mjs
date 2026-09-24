import { boundaryRule } from "./boundaries.mjs";
export function applicationConfig(tseslint, vue, root, app) {
  return [
    { ignores: ["dist/**", "test-results/**", "playwright-report/**"] },
    ...tseslint.configs.recommended,
    ...vue.configs["flat/essential"],
    {
      files: ["**/*.vue"],
      languageOptions: { parserOptions: { parser: tseslint.parser } },
    },
    {
      files: [app === "package" ? "**/*.{ts,vue}" : "src/**/*.{ts,vue}"],
      plugins: {
        marketmesh: { rules: { boundaries: boundaryRule(root, app) } },
      },
      rules: {
        "marketmesh/boundaries": "error",
        "no-console": "error",
        "no-eval": "error",
        "vue/no-v-html": "error",
      },
    },
    {
      files: ["src/**/*.test.ts", "src/testing/**"],
      rules: { "marketmesh/boundaries": "off" },
    },
  ];
}
