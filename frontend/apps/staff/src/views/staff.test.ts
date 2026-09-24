import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createMemoryHistory } from "vue-router";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Invite, StaffApi } from "../api";
import { staffApiKey } from "../context";
import { createStaffRouter } from "../router";

const invite = (overrides: Partial<Invite> = {}): Invite => ({
  state: "active",
  role: "модератор",
  inviterName: "Анна Соколова",
  ...overrides,
});

function fixture() {
  const staffApi: StaffApi = {
    getSession: vi
      .fn()
      .mockRejectedValue(new ConnectError("expired", Code.Unauthenticated)),
    logout: vi.fn().mockResolvedValue(undefined),
    startSso: vi.fn().mockResolvedValue("https://idp.corp.test/authorize"),
    getInvite: vi.fn().mockResolvedValue(invite()),
    acceptInvite: vi.fn().mockResolvedValue(undefined),
  };
  return { staffApi };
}

const mounted: VueWrapper[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
});

async function open(staffApi: StaffApi, path: string) {
  const router = createStaffRouter(createMemoryHistory());
  await router.push(path);
  await router.isReady();
  const wrapper = mount(
    { template: "<RouterView />" },
    {
      global: {
        plugins: [router],
        provide: { [staffApiKey as symbol]: staffApi },
      },
    },
  );
  mounted.push(wrapper);
  await flushPromises();
  return { wrapper, router };
}

function button(wrapper: VueWrapper, text: string) {
  const found = wrapper
    .findAll("button")
    .find((candidate) => candidate.text().includes(text));
  if (!found) throw new Error(`Missing button ${text}`);
  return found;
}

describe("staff login", () => {
  it("starts the SSO redirect from the work account button", async () => {
    const { staffApi } = fixture();
    const { wrapper } = await open(staffApi, "/staff/login");
    expect(wrapper.find("h1").text()).toBe("Войдите через рабочий аккаунт.");
    expect(wrapper.find("input").exists()).toBe(false);
    await button(wrapper, "Войти через рабочий аккаунт").trigger("click");
    await flushPromises();
    expect(staffApi.startSso).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
  });

  it.each([Code.PermissionDenied])(
    "explains the corporate network requirement on %s",
    async (code) => {
      const { staffApi } = fixture();
      vi.mocked(staffApi.startSso).mockRejectedValue(
        new ConnectError("outside", code),
      );
      const { wrapper } = await open(staffApi, "/staff/login");
      await button(wrapper, "Войти через рабочий аккаунт").trigger("click");
      await flushPromises();
      expect(wrapper.find("h1").text()).toBe(
        "Портал открыт только из корпоративной сети.",
      );
      expect(wrapper.text()).toContain("Подключитесь к VPN");
    },
  );

  it("shows a plain server error for other failures", async () => {
    const { staffApi } = fixture();
    vi.mocked(staffApi.startSso).mockRejectedValue(
      new ConnectError("boom", Code.Internal),
    );
    const { wrapper } = await open(staffApi, "/staff/login");
    await button(wrapper, "Войти через рабочий аккаунт").trigger("click");
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain(
      "Не удалось подключиться к серверу.",
    );
  });

  it("announces the expired session from the query until the next attempt", async () => {
    const { staffApi } = fixture();
    const { wrapper } = await open(staffApi, "/staff/login?expired=1");
    expect(wrapper.find('[role="alert"]').text()).toContain(
      "Сессия завершена.",
    );
    await button(wrapper, "Войти через рабочий аккаунт").trigger("click");
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
  });
});

describe("staff invite", () => {
  it("explains the missing token without calling the API", async () => {
    const { staffApi } = fixture();
    const { wrapper } = await open(staffApi, "/staff/invite");
    expect(wrapper.find("h1").text()).toBe("Ссылка неполная.");
    expect(staffApi.getInvite).not.toHaveBeenCalled();
  });

  it("accepts an active invite and opens the already authenticated portal", async () => {
    const { staffApi } = fixture();
    const { wrapper } = await open(staffApi, "/staff/invite?token=abc");
    expect(wrapper.find("h1").text()).toBe("Вас пригласили в портал.");
    expect(wrapper.text()).toContain("Вам открыли доступ с ролью модератор.");
    expect(wrapper.text()).toContain("Анна Соколова");
    await button(wrapper, "Принять приглашение").trigger("click");
    await flushPromises();
    expect(staffApi.acceptInvite).toHaveBeenCalledWith("abc");
    expect(wrapper.find("h1").text()).toBe("Доступ открыт.");
    expect(wrapper.find('a[href="/staff"]').exists()).toBe(true);
  });

  it.each([
    {
      state: "expired" as const,
      title: "Срок приглашения истёк.",
      text: "Попросите администратора отправить новое приглашение.",
    },
    {
      state: "used" as const,
      title: "Приглашение уже использовано.",
      text: "просто войдите",
    },
    {
      state: "wrongAccount" as const,
      title: "Приглашение для другой почты.",
      text: "выдано для другого адреса",
    },
  ])("shows the $state invite state", async ({ state, title, text }) => {
    const { staffApi } = fixture();
    vi.mocked(staffApi.getInvite).mockResolvedValue(
      invite({ state, role: "", inviterName: "" }),
    );
    const { wrapper } = await open(staffApi, "/staff/invite?token=abc");
    expect(wrapper.find("h1").text()).toBe(title);
    expect(wrapper.text()).toContain(text);
    expect(staffApi.acceptInvite).not.toHaveBeenCalled();
  });

  it("recovers from a lookup failure through the retry button", async () => {
    const { staffApi } = fixture();
    vi.mocked(staffApi.getInvite).mockRejectedValueOnce(
      new ConnectError("boom", Code.Internal),
    );
    const { wrapper } = await open(staffApi, "/staff/invite?token=abc");
    expect(wrapper.find("h1").text()).toBe("Не удалось проверить приглашение.");
    await button(wrapper, "Проверить снова").trigger("click");
    await flushPromises();
    expect(wrapper.find("h1").text()).toBe("Вас пригласили в портал.");
  });
});

describe("staff session lifetime and invitation navigation", () => {
  it("loads a different fragment invite without keeping the previous result", async () => {
    const { staffApi } = fixture();
    const { wrapper, router } = await open(
      staffApi,
      "/staff/invite#token=first",
    );
    expect(wrapper.text()).toContain("Вас пригласили в портал.");
    vi.mocked(staffApi.getInvite).mockResolvedValue(
      invite({ state: "expired", role: "", inviterName: "" }),
    );
    await router.push("/staff/invite#token=second");
    await flushPromises();
    expect(staffApi.getInvite).toHaveBeenLastCalledWith("second");
    expect(wrapper.text()).toContain("Срок приглашения истёк.");
    expect(wrapper.text()).not.toContain("Анна Соколова");
  });
  it("clears private staff identity at the returned deadline", async () => {
    vi.useFakeTimers();
    try {
      const { staffApi } = fixture();
      vi.mocked(staffApi.getSession).mockResolvedValue({
        email: "private@example.test",
        displayName: "Private employee",
        role: "support",
        expiresAt: Date.now() + 500,
      });
      const { wrapper, router } = await open(staffApi, "/staff");
      expect(wrapper.text()).toContain("private@example.test");
      await vi.advanceTimersByTimeAsync(501);
      await flushPromises();
      expect(router.currentRoute.value.fullPath).toBe("/staff/login?expired=1");
      expect(wrapper.text()).not.toContain("private@example.test");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("stale staff SSO", () => {
  it.each(["/staff/invite#token=second", "/staff/login"])(
    "does not start SSO after leaving an invite for %s",
    async (destination) => {
      const { staffApi } = fixture();
      vi.mocked(staffApi.getInvite).mockRejectedValue(
        new ConnectError("expired", Code.Unauthenticated),
      );
      let release!: () => void;
      vi.mocked(staffApi.logout).mockImplementation(
        () =>
          new Promise<void>((resolve) => {
            release = resolve;
          }),
      );
      const { wrapper, router } = await open(
        staffApi,
        "/staff/invite#token=first",
      );
      await button(wrapper, "Войти через рабочий аккаунт").trigger("click");
      await router.push(destination);
      release();
      await flushPromises();
      expect(staffApi.startSso).not.toHaveBeenCalled();
    },
  );
});
