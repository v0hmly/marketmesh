import { inject, type InjectionKey } from 'vue';
import type { SessionController } from './session';

export const sessionKey: InjectionKey<SessionController> = Symbol('marketmesh-session');

export function useSession(): SessionController {
  const session = inject(sessionKey);
  if (!session) throw new Error('Session controller is missing');
  return session;
}
