import { create, toBinary } from "@bufbuild/protobuf";
import { ConnectError } from "@connectrpc/connect";
import {
  GetInviteResponseSchema,
  InviteState,
  StartSsoResponseSchema,
} from "@marketmesh/api/staff/v1/staff_pb";
import { describe, expect, it, vi } from "vitest";
import { createStaffApi } from "./api";

const inviteResponse = (state: InviteState) =>
  new Response(
    toBinary(
      GetInviteResponseSchema,
      create(GetInviteResponseSchema, {
        state,
        role: "модератор",
        inviterName: "Анна Соколова",
      }),
    ),
    { headers: { "content-type": "application/proto" } },
  );

describe("staff API facade", () => {
  it("returns only https SSO redirects", async () => {
    const insecure = new Response(
      toBinary(
        StartSsoResponseSchema,
        create(StartSsoResponseSchema, {
          authorizeUrl: "http://idp.corp.test/authorize",
        }),
      ),
      { headers: { "content-type": "application/proto" } },
    );
    const fetcher = vi.fn<typeof fetch>(async () =>
      Promise.resolve(insecure.clone()),
    );
    await expect(
      createStaffApi("https://staff.marketmesh.test", fetcher).startSso(),
    ).rejects.toBeInstanceOf(ConnectError);
  });

  it("maps invite states and requires role data for active invites", async () => {
    const fetcher = vi.fn<typeof fetch>(async () =>
      Promise.resolve(inviteResponse(InviteState.USED)),
    );
    const api = createStaffApi("https://staff.marketmesh.test", fetcher);
    expect((await api.getInvite("token")).state).toBe("used");

    const incomplete = new Response(
      toBinary(
        GetInviteResponseSchema,
        create(GetInviteResponseSchema, { state: InviteState.ACTIVE }),
      ),
      { headers: { "content-type": "application/proto" } },
    );
    const badFetcher = vi.fn<typeof fetch>(async () =>
      Promise.resolve(incomplete.clone()),
    );
    await expect(
      createStaffApi("https://staff.marketmesh.test", badFetcher).getInvite(
        "token",
      ),
    ).rejects.toBeInstanceOf(ConnectError);
  });
});

describe("staff ownership changes", () => {
  it("rejects a response that finishes after another session mutation", async () => {
    let release!: (response: Response) => void;
    let revision = 0;
    const fetcher = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    );
    const api = createStaffApi("https://staff.marketmesh.test", fetcher, {
      revision: () => revision,
      invalidate: () => {
        revision++;
      },
    });
    const read = api.getInvite("opaque-invite");
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalled());
    revision++;
    release(inviteResponse(InviteState.ACTIVE));
    await expect(read).rejects.toMatchObject({ code: 16 });
  });
});

it("rejects an SSO redirect completed after remote session invalidation", async () => {
  let release!: (response: Response) => void;
  let revision = 0;
  const fetcher = vi.fn<typeof fetch>(
    () =>
      new Promise<Response>((resolve) => {
        release = resolve;
      }),
  );
  const api = createStaffApi("https://staff.marketmesh.test", fetcher, {
    revision: () => revision,
    invalidate: () => {
      revision++;
    },
  });
  const result = api.startSso();
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalled());
  revision++;
  release(
    new Response(
      toBinary(
        StartSsoResponseSchema,
        create(StartSsoResponseSchema, {
          authorizeUrl: "https://idp.corp.test/authorize",
        }),
      ),
      { headers: { "content-type": "application/proto" } },
    ),
  );
  await expect(result).rejects.toMatchObject({ code: 16 });
});
