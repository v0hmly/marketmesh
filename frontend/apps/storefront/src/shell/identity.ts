import type { SessionApi } from './session/contracts';
import type { AccountApi } from '../modules/account/api/types';
import { isProfilePending } from '../modules/account/api/errors';

/** Existing GetMe is the verified identity contract until Auth exposes an equivalent.
 * Only its owner crosses into the session coordinator; profile fields stay in Account.
 * PROFILE_NOT_READY confirms authentication but does not invent an owner client-side.
 */
export function accountIdentityProbe(
  account: Pick<AccountApi, 'getProfile'>,
): SessionApi['getIdentity'] {
  return async () => {
    try {
      const { subjectId } = await account.getProfile();
      return { subjectId };
    } catch (error) {
      if (isProfilePending(error)) return null;
      throw error;
    }
  };
}
