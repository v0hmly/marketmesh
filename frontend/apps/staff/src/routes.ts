import type { RouteRecordRaw } from "vue-router";
const StaffLoginView = () => import("./views/StaffLoginView.vue");
const StaffInviteView = () => import("./views/StaffInviteView.vue");

/** Маршруты области сотрудника; композицию выполняет shell/router. */
export const staffRoutes: RouteRecordRaw[] = [
  {
    path: "/staff",
    name: "staff-home",
    component: () => import("./views/StaffHomeView.vue"),
  },
  {
    path: "/staff/login",
    name: "staff-login",
    component: StaffLoginView,
  },
  {
    path: "/staff/invite",
    name: "staff-invite",
    component: StaffInviteView,
  },
];
