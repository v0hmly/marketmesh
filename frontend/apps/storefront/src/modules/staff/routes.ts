import type { RouteRecordRaw } from 'vue-router';
import { staffEnabled } from '../../shared/features';
import StaffLoginView from './views/StaffLoginView.vue';
import StaffInviteView from './views/StaffInviteView.vue';

/** Маршруты области сотрудника; композицию выполняет shell/router. */
export const staffRoutes: RouteRecordRaw[] = staffEnabled
  ? [
      {
        path: '/staff/login',
        name: 'staff-login',
        component: StaffLoginView,
        meta: { area: 'staff' },
      },
      {
        path: '/staff/invite',
        name: 'staff-invite',
        component: StaffInviteView,
        meta: { area: 'staff' },
      },
    ]
  : [];
