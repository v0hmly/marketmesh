import { Code, ConnectError, createClient } from "@connectrpc/connect";
import {
  InviteState,
  StaffService,
  type GetInviteResponse,
} from "@marketmesh/api/staff/v1/staff_pb";
import { createPublicTransport } from "@marketmesh/browser-client/transport";

export interface StaffSession {
  email: string;
  displayName: string;
  role: string;
  expiresAt: number;
}
export type InviteOutcome = "active" | "expired" | "used" | "wrongAccount";
export interface Invite {
  state: InviteOutcome;
  role: string;
  inviterName: string;
}

/** Only browser-public staff RPCs; the portal itself lives in the corporate network. */
export interface StaffApi {
  startSso(inviteToken?: string): Promise<string>;
  getSession(): Promise<StaffSession>;
  logout(): Promise<void>;
  getInvite(inviteToken: string): Promise<Invite>;
  acceptInvite(inviteToken: string): Promise<void>;
}

const wireStates = {
  [InviteState.ACTIVE]: "active",
  [InviteState.EXPIRED]: "expired",
  [InviteState.USED]: "used",
  [InviteState.WRONG_ACCOUNT]: "wrongAccount",
} as const;

function validInvite(response: GetInviteResponse | undefined): Invite {
  if (!response || !(response.state in wireStates))
    throw new ConnectError("Invalid invite response", Code.DataLoss);
  const state = wireStates[response.state as keyof typeof wireStates];
  if (state === "active" && (!response.role || !response.inviterName))
    throw new ConnectError("Invalid invite response", Code.DataLoss);
  return { state, role: response.role, inviterName: response.inviterName };
}

/** Фасад области сотрудника поверх сгенерированного staff.v1 клиента (MM-81). */
export function createStaffApi(
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
  ownership = { revision: () => 0, invalidate: () => {} },
): StaffApi {
  async function owned<T>(action: () => Promise<T>): Promise<T> {
    const revision = ownership.revision();
    try {
      const result = await action();
      if (revision !== ownership.revision())
        throw new ConnectError("Staff session changed", Code.Unauthenticated);
      return result;
    } catch (error) {
      if (revision !== ownership.revision())
        throw new ConnectError("Staff session changed", Code.Unauthenticated);
      throw error;
    }
  }
  const client = createClient(
    StaffService,
    createPublicTransport(origin, fetcher),
  );
  return {
    async startSso(inviteToken = "") {
      ownership.invalidate();
      const { authorizeUrl } = await owned(() =>
        client.startSso({ inviteToken }),
      );
      let url: URL;
      try {
        url = new URL(authorizeUrl);
      } catch {
        throw new ConnectError("Invalid SSO redirect response", Code.DataLoss);
      }
      if (url.protocol !== "https:")
        throw new ConnectError("Invalid SSO redirect response", Code.DataLoss);
      return authorizeUrl;
    },
    async getSession() {
      const result = await owned(() => client.getSession({}));
      const expiresAt = Number(result.expiresAtUnix) * 1000;
      if (
        !result.email ||
        !Number.isSafeInteger(expiresAt) ||
        expiresAt <= Date.now() ||
        !["", "support", "moderator", "admin"].includes(result.role)
      )
        throw new ConnectError("Invalid staff session", Code.DataLoss);
      return {
        email: result.email,
        displayName: result.displayName,
        role: result.role,
        expiresAt,
      };
    },
    async logout() {
      ownership.invalidate();
      await owned(() => client.logout({}));
    },
    async getInvite(inviteToken) {
      return validInvite(await owned(() => client.getInvite({ inviteToken })));
    },
    async acceptInvite(inviteToken) {
      await owned(() => client.acceptInvite({ inviteToken }));
    },
  };
}
