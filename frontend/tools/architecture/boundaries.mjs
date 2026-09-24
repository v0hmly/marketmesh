import path from "node:path";

const packages = new Set([
  "vue",
  "vue-router",
  "@connectrpc/connect",
  "@connectrpc/connect-web",
  "@bufbuild/protobuf",
  "@marketmesh/browser-client/transport",
  "@marketmesh/browser-client/recovery",
  "@marketmesh/design-system/style.css",
  "@marketmesh/design-system/BrandMark.vue",
  "@marketmesh/design-system/ConfirmDialog.vue",
]);
const schema =
  /^@marketmesh\/api\/(auth|user|files|seller|staff)\/v1\/([a-z_]+)_pb$/;

/** Runtime dependency policy; tests exercise the same resolver used by ESLint. */
export function allowedImport({
  source,
  specifier,
  app = "storefront",
  dynamic = false,
  star = false,
}) {
  if (typeof specifier !== "string" || star) return false;
  if (packages.has(specifier)) return true;
  if (app === "package") {
    if (!specifier.startsWith(".")) return false;
    const target = path.posix.normalize(
      path.posix.join(path.posix.dirname(source), specifier),
    );
    return !target.startsWith("../");
  }
  if (specifier === "@marketmesh/api/google/rpc/error_details_pb")
    return /(?:^|\/)errors\.ts$/.test(source);
  if (specifier.startsWith("@marketmesh/api/")) {
    const match = schema.exec(specifier);
    if (!match || dynamic || match[1] !== match[2]) return false;
    if (app === "staff") return match[1] === "staff" && source === "api.ts";
    const owner = /^modules\/([^/]+)\//.exec(source)?.[1];
    const domains = {
      auth: ["auth"],
      account: ["user", "files"],
      seller: ["seller"],
    };
    return Boolean(
      owner &&
      domains[owner]?.includes(match[1]) &&
      /(?:^|\/)(api|client|types)\.ts$/.test(source),
    );
  }
  if (!specifier.startsWith(".")) return false;
  const target = path.posix.normalize(
    path.posix.join(path.posix.dirname(source), specifier),
  );
  if (
    target.startsWith("../") ||
    /(^|\/)(testing|__tests__)(\/|$)|\.test\./.test(target)
  )
    return false;
  if (app === "staff") return true;
  const sourceOwner = /^modules\/([^/]+)\//.exec(source)?.[1];
  const targetOwner = /^modules\/([^/]+)\//.exec(target)?.[1];
  if (source.startsWith("shared/")) return target.startsWith("shared/");
  if (/^shell\/context(?:\.ts)?$/.test(source))
    return /^shell\/session(?:\/|$)/.test(target);
  if (source.startsWith("shell/session/"))
    return target.startsWith("shell/session/");
  if (sourceOwner) {
    if (targetOwner && sourceOwner !== targetOwner)
      return (
        ["account", "seller"].includes(sourceOwner) &&
        targetOwner === "auth" &&
        /^modules\/auth\/(?:public|ui|forms)(?:\.ts)?$/.test(target)
      );
    if (target.startsWith("shell/"))
      return /^shell\/(context|session(?:\/(?:index|contracts))?)(?:\.ts)?$/.test(
        target,
      );
    if (!targetOwner && !target.startsWith("shared/")) return false;
  }
  return true;
}

export function boundaryRule(sourceRoot, app) {
  return {
    meta: {
      type: "problem",
      schema: [],
      messages: {
        forbidden:
          "Dependency crosses the declared application/module boundary.",
      },
    },
    create(context) {
      const source = path
        .relative(sourceRoot, context.filename)
        .split(path.sep)
        .join("/");
      function check(node) {
        if (!node.source) return;
        if (
          !allowedImport({
            source,
            specifier: node.source.value,
            app,
            dynamic: node.type === "ImportExpression",
            star: node.type === "ExportAllDeclaration",
          })
        )
          context.report({ node, messageId: "forbidden" });
        // AuthService is public; AuthSessionService from the same schema is internal.
        if (
          node.source.value === "@marketmesh/api/auth/v1/auth_pb" &&
          node.specifiers?.some(
            (s) =>
              s.type !== "ImportSpecifier" || s.imported.name !== "AuthService",
          )
        )
          context.report({ node, messageId: "forbidden" });
      }
      return {
        ImportDeclaration: check,
        ExportNamedDeclaration: check,
        ExportAllDeclaration: check,
        ImportExpression: check,
      };
    },
  };
}
