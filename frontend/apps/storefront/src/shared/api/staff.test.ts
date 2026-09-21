import { create, toBinary } from '@bufbuild/protobuf';
import { ConnectError } from '@connectrpc/connect';
import {
  GetInviteResponseSchema,
  InviteState,
  StartSsoResponseSchema,
} from '@marketmesh/api/staff/v1/staff_pb';
import { describe, expect, it, vi } from 'vitest';
import { createStaffApi } from './staff';

const inviteResponse = (state: InviteState) =>
  new Response(
    toBinary(
      GetInviteResponseSchema,
      create(GetInviteResponseSchema, {
        state,
        role: 'модератор',
        inviterName: 'Анна Соколова',
      }),
    ),
    { headers: { 'content-type': 'application/proto' } },
  );

describe('staff API facade', () => {
  it('returns only https SSO redirects', async () => {
    const insecure = new Response(
      toBinary(
        StartSsoResponseSchema,
        create(StartSsoResponseSchema, { authorizeUrl: 'http://idp.corp.test/authorize' }),
      ),
      { headers: { 'content-type': 'application/proto' } },
    );
    const fetcher = vi.fn<typeof fetch>(async () => Promise.resolve(insecure.clone()));
    await expect(
      createStaffApi('https://staff.marketmesh.test', fetcher).startSso(),
    ).rejects.toBeInstanceOf(ConnectError);
  });

  it('maps invite states and requires role data for active invites', async () => {
    const fetcher = vi.fn<typeof fetch>(async () =>
      Promise.resolve(inviteResponse(InviteState.USED)),
    );
    const api = createStaffApi('https://staff.marketmesh.test', fetcher);
    expect((await api.getInvite('token')).state).toBe('used');

    const incomplete = new Response(
      toBinary(
        GetInviteResponseSchema,
        create(GetInviteResponseSchema, { state: InviteState.ACTIVE }),
      ),
      { headers: { 'content-type': 'application/proto' } },
    );
    const badFetcher = vi.fn<typeof fetch>(async () => Promise.resolve(incomplete.clone()));
    await expect(
      createStaffApi('https://staff.marketmesh.test', badFetcher).getInvite('token'),
    ).rejects.toBeInstanceOf(ConnectError);
  });
});
