import { Code, ConnectError, createClient } from '@connectrpc/connect';
import {
  InviteState,
  StaffService,
  type GetInviteResponse,
} from '@marketmesh/api/staff/v1/staff_pb';
import { createPublicTransport } from './transport';

export type InviteOutcome = 'active' | 'expired' | 'used' | 'wrongAccount';
export interface Invite {
  state: InviteOutcome;
  role: string;
  inviterName: string;
}

/** Only browser-public staff RPCs; the portal itself lives in the corporate network. */
export interface StaffApi {
  startSso(): Promise<string>;
  getInvite(inviteToken: string): Promise<Invite>;
  acceptInvite(inviteToken: string): Promise<void>;
}

const wireStates = {
  [InviteState.ACTIVE]: 'active',
  [InviteState.EXPIRED]: 'expired',
  [InviteState.USED]: 'used',
  [InviteState.WRONG_ACCOUNT]: 'wrongAccount',
} as const;

function validInvite(response: GetInviteResponse | undefined): Invite {
  if (!response || !(response.state in wireStates))
    throw new ConnectError('Invalid invite response', Code.DataLoss);
  const state = wireStates[response.state as keyof typeof wireStates];
  if (state === 'active' && (!response.role || !response.inviterName))
    throw new ConnectError('Invalid invite response', Code.DataLoss);
  return { state, role: response.role, inviterName: response.inviterName };
}

/** Фасад области сотрудника поверх сгенерированного staff.v1 клиента (MM-81). */
export function createStaffApi(
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
): StaffApi {
  const client = createClient(StaffService, createPublicTransport(origin, fetcher));
  return {
    async startSso() {
      const { authorizeUrl } = await client.startSso({});
      let url: URL;
      try {
        url = new URL(authorizeUrl);
      } catch {
        throw new ConnectError('Invalid SSO redirect response', Code.DataLoss);
      }
      if (url.protocol !== 'https:')
        throw new ConnectError('Invalid SSO redirect response', Code.DataLoss);
      return authorizeUrl;
    },
    async getInvite(inviteToken) {
      return validInvite(await client.getInvite({ inviteToken }));
    },
    async acceptInvite(inviteToken) {
      await client.acceptInvite({ inviteToken });
    },
  };
}
