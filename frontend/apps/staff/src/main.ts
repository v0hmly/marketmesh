import { createRouteRecovery } from "@marketmesh/browser-client/recovery";
import { recoveryKey } from "./recovery";
import { createApp, shallowRef } from "vue";
import App from "./App.vue";
import { createStaffApi } from "./api";
import { staffApiKey, invalidatedKey } from "./context";
import { createStaffRouter } from "./router";
import "@marketmesh/design-system/style.css";
const app = createApp(App);
const router = createStaffRouter();
let revision = 0;
const invalidated = shallowRef(false);
app.provide(invalidatedKey, invalidated);
const channel =
  typeof BroadcastChannel === "undefined"
    ? null
    : new BroadcastChannel("marketmesh:staff-session");
if (channel)
  channel.onmessage = () => {
    revision++;
    // Clear private content before the lazy login chunk is available.
    invalidated.value = true;
    void router
      .replace("/staff/login?expired=1")
      .then(() => {
        if (router.currentRoute.value.path === "/staff/login")
          invalidated.value = false;
      })
      .catch(() => {
        /* Route recovery keeps private content hidden. */
      });
  };
app.provide(
  staffApiKey,
  createStaffApi(window.location.origin, globalThis.fetch.bind(globalThis), {
    revision: () => revision,
    invalidate: () => {
      revision++;
      channel?.postMessage("invalidate");
    },
  }),
);
app.provide(recoveryKey, createRouteRecovery(router));
app.use(router);
app.mount("#app");
