import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { InviteState, StaffService } from "@marketmesh/api/staff/v1/staff_pb";
import AxeBuilder from "@axe-core/playwright";
import {
  expect,
  test,
  type BrowserContext,
  type Route,
} from "@playwright/test";

// Contract-shaped UI fixture, deliberately separate from the full backend E2E.
async function browserApi(context: BrowserContext) {
  async function jsonError(route: Route, code: string, status: number) {
    await route.fulfill({
      status,
      headers: {
        "content-type": "application/json",
        "cache-control": "no-store",
      },
      body: JSON.stringify({ code, message: "Request failed" }),
    });
  }
  await context.route("**/auth.v1.AuthService/**", (route) =>
    jsonError(route, "unimplemented", 501),
  );
  await context.route("**/user.v1.UserService/**", (route) =>
    jsonError(route, "unauthenticated", 401),
  );
  await context.route("**/staff.v1.StaffService/**", async (route) => {
    const method = new URL(route.request().url()).pathname.split("/").at(-1);
    if (method === "GetSession") {
      await route.fulfill({
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          email: "employee@example.test",
          displayName: "Сотрудник",
          role: "support",
          expiresAtUnix: String(Math.floor(Date.now() / 1000) + 1800),
        }),
      });
      return;
    }
    if (method === "StartSso") {
      await route.fulfill({
        headers: {
          "content-type": "application/proto",
          "cache-control": "no-store",
        },
        body: Buffer.from(
          toBinary(
            StaffService.method.startSso.output,
            create(StaffService.method.startSso.output, {
              authorizeUrl: "https://idp.corp.test/authorize",
            }),
          ),
        ),
      });
      return;
    }
    if (method === "GetInvite") {
      const input = fromBinary(
        StaffService.method.getInvite.input,
        route.request().postDataBuffer()!,
      );
      const state =
        input.inviteToken === "active-token"
          ? InviteState.ACTIVE
          : input.inviteToken === "expired-token"
            ? InviteState.EXPIRED
            : InviteState.USED;
      await route.fulfill({
        headers: {
          "content-type": "application/proto",
          "cache-control": "no-store",
        },
        body: Buffer.from(
          toBinary(
            StaffService.method.getInvite.output,
            create(StaffService.method.getInvite.output, {
              state,
              role: "модератор",
              inviterName: "Анна Соколова",
            }),
          ),
        ),
      });
      return;
    }
    if (method === "AcceptInvite") {
      await route.fulfill({
        headers: {
          "content-type": "application/proto",
          "cache-control": "no-store",
        },
        body: Buffer.alloc(0),
      });
      return;
    }
    await jsonError(route, "unimplemented", 501);
  });
}

test("staff login starts the SSO redirect and stays accessible", async ({
  page,
  context,
}) => {
  await browserApi(context);
  await page.goto("/staff/login");
  await expect(
    page.getByRole("heading", { name: "Войдите через рабочий аккаунт." }),
  ).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  const ssoRequest = page.waitForRequest("**/staff.v1.StaffService/StartSso");
  await page
    .getByRole("button", { name: "Войти через рабочий аккаунт" })
    .click();
  // The redirect itself is not followed: the IdP host does not exist in this fixture.
  await ssoRequest;
});

test("staff invite accepts an active token and stays accessible", async ({
  page,
  context,
}) => {
  await browserApi(context);
  await page.goto("/staff/invite?token=active-token");
  await expect(
    page.getByRole("heading", { name: "Вас пригласили в портал." }),
  ).toBeVisible();
  await expect(
    page.getByText("Вам открыли доступ с ролью модератор."),
  ).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByRole("button", { name: "Принять приглашение" }).click();
  await expect(
    page.getByRole("heading", { name: "Доступ открыт." }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Открыть портал" }).click();
  await expect(page).toHaveURL(/\/staff$/);
  await expect(
    page.getByRole("heading", { name: "Рабочий аккаунт." }),
  ).toBeVisible();
});

test("staff invite shows the expired state and stays accessible", async ({
  page,
  context,
}) => {
  await browserApi(context);
  await page.goto("/staff/invite?token=expired-token");
  await expect(
    page.getByRole("heading", { name: "Срок приглашения истёк." }),
  ).toBeVisible();
  await expect(
    page.getByText("Попросите администратора отправить новое приглашение."),
  ).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

for (const leave of ["route", "remote invalidation"] as const) {
  test(`late SSO cannot redirect after ${leave}`, async ({ page, context }) => {
    await browserApi(context);
    let finish!: () => void;
    const held = new Promise<void>((resolve) => {
      finish = resolve;
    });
    let pending = false;
    await context.route("**/staff.v1.StaffService/StartSso", async (route) => {
      pending = true;
      await held;
      await route.fulfill({
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          authorizeUrl: "https://idp.corp.test/authorize",
        }),
      });
    });
    const redirects: string[] = [];
    await context.route("https://idp.corp.test/**", async (route) => {
      redirects.push(route.request().url());
      await route.abort();
    });
    await page.goto("/staff/login");
    await page
      .getByRole("button", { name: "Войти через рабочий аккаунт" })
      .click();
    await expect.poll(() => pending).toBe(true);
    if (leave === "route") {
      await page.getByRole("link", { name: "Откройте его" }).click();
      await expect(page).toHaveURL(/\/staff\/invite$/);
    } else {
      await page.evaluate(() => {
        const channel = new BroadcastChannel("marketmesh:staff-session");
        channel.postMessage("invalidate");
        channel.close();
      });
      await expect(page).toHaveURL(/expired=1$/);
    }
    const reply = page.waitForResponse("**/staff.v1.StaffService/StartSso");
    finish();
    await reply;
    // A subsequent browser turn observes processing of the delayed response.
    await expect(page.getByRole("heading")).toBeVisible();
    expect(redirects).toEqual([]);
    await expect(page).toHaveURL(
      leave === "route" ? /\/staff\/invite$/ : /expired=1$/,
    );
  });
}
