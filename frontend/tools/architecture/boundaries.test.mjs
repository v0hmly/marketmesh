import { test } from "node:test";
import assert from "node:assert/strict";
import { allowedImport } from "./boundaries.mjs";
const check = (source, specifier, options = {}) =>
  allowedImport({ source, specifier, ...options });
test("forbids private cross-domain imports and shared/context escape paths", () => {
  for (const specifier of [
    "../../seller/api",
    "../../../shared/../modules/seller/api",
    "/src/modules/seller/api",
    "@/modules/seller/api",
    "#seller",
    "file:///tmp/seller.ts",
  ])
    assert.equal(
      check("modules/account/profile/View.vue", specifier),
      false,
      specifier,
    );
  assert.equal(check("shared/index.ts", "../modules/seller/api"), false);
  assert.equal(
    check("shell/session/index.ts", "../../modules/account/api/types"),
    false,
  );
  assert.equal(
    check("modules/account/api/client.ts", "../../../testing/session"),
    false,
  );
  assert.equal(
    check("modules/account/api/client.ts", "../../../shell/identity"),
    false,
  );
  assert.equal(
    check("modules/account/api/client.ts", "../../../../staff/src/api"),
    false,
  );
});
test("dynamic imports and reexports obey the same boundary", () => {
  assert.equal(
    check("modules/account/index.ts", "../seller/api", { dynamic: true }),
    false,
  );
  assert.equal(
    check("modules/account/index.ts", "../seller/api", { star: true }),
    false,
  );
  assert.equal(
    check("modules/account/index.ts", undefined, { dynamic: true }),
    false,
  );
  assert.equal(
    check("modules/account/routes.ts", "./profile/IdView.vue", {
      dynamic: true,
    }),
    true,
  );
  assert.equal(
    check("modules/account/profile/IdView.vue", "../../auth/public"),
    true,
  );
});
test("staff cannot import buyer code or internal protobuf schemas", () => {
  assert.equal(
    check("api.ts", "../../storefront/src/shell/session", { app: "staff" }),
    false,
  );
  assert.equal(
    check("api.ts", "@marketmesh/api/auth/v1/auth_pb", { app: "staff" }),
    false,
  );
  assert.equal(
    check("api.ts", "@marketmesh/api/staff/v1/staff_pb", { app: "staff" }),
    true,
  );
  for (const schema of [
    "auth/v1/session_pb",
    "auth/v1/events_pb",
    "tunnel/v1/tunnel_pb",
    "gateway/v1/gateway_pb",
    "files/v1/avatar_internal_pb",
  ])
    assert.equal(
      check("modules/auth/api.ts", "@marketmesh/api/" + schema),
      false,
    );
  assert.equal(
    check("modules/account/api/client.ts", "@marketmesh/api/staff/v1/staff_pb"),
    false,
  );
  assert.equal(
    check("modules/account/api/client.ts", "@marketmesh/api/user/v1/user_pb"),
    true,
  );
  assert.equal(
    check(
      "modules/account/profile/IdView.vue",
      "@marketmesh/api/user/v1/user_pb",
    ),
    false,
  );
});

// Exercise the parser/listener wiring as well as the pure policy: named reexports
// and computed imports must not silently disappear from lint coverage.
test("real ESLint rejects escape routes in applications and shared packages", async () => {
  const { createRequire } = await import("node:module");
  const { fileURLToPath } = await import("node:url");
  const require = createRequire(
    new URL("../../apps/storefront/package.json", import.meta.url),
  );
  const { ESLint } = require("eslint");
  const { boundaryRule } = await import("./boundaries.mjs");
  const root = fileURLToPath(new URL("../../", import.meta.url));
  for (const [app, source, code] of [
    [
      "storefront",
      "shell/context.ts",
      "export { createSellerApi } from '../modules/seller/api'",
    ],
    [
      "storefront",
      "shared/api.ts",
      "export { createSellerApi } from '../modules/seller/api'",
    ],
    ["storefront", "modules/account/view.ts", "import('../seller/api')"],
    ["storefront", "modules/account/view.ts", "import(target)"],
    [
      "storefront",
      "modules/account/view.ts",
      "export * from '../../shared/validation'",
    ],
    [
      "package",
      "transport.ts",
      "export { createSellerApi } from '../../apps/storefront/src/modules/seller/api'",
    ],
    [
      "package",
      "transport.ts",
      "import { StaffService } from '@marketmesh/api/staff/v1/staff_pb'",
    ],
  ]) {
    const eslint = new ESLint({
      cwd: root,
      overrideConfigFile: true,
      overrideConfig: [
        {
          files: ["**/*.ts"],
          plugins: {
            marketmesh: { rules: { boundaries: boundaryRule(root, app) } },
          },
          rules: { "marketmesh/boundaries": "error" },
        },
      ],
    });
    const [result] = await eslint.lintText(code, { filePath: root + source });
    assert.equal(result.errorCount, 1, `${app}: ${code}`);
  }
});
