import { createRouter, createWebHistory, type RouterHistory } from "vue-router";
import { staffRoutes } from "./routes";
export function createStaffRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({
    history,
    routes: [
      { path: "/", redirect: "/staff" },
      ...staffRoutes,
      { path: "/:pathMatch(.*)*", redirect: "/staff/login" },
    ],
  });
}
