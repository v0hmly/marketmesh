import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
const fixture = JSON.parse(
  readFileSync("/staff-fixture/fixture.json", "utf8"),
) as {
  employeePassword: string;
  otherPassword: string;
  active: string;
  wrong: string;
  expired: string;
};
async function login(page: Page, who: "employee" | "other" = "employee") {
  await page
    .getByRole("button", { name: "Войти через рабочий аккаунт" })
    .click();
  await expect(page).toHaveURL(/^https:\/\/oidc\.localhost:18445\/authorize\?/);
  await page.getByLabel("Рабочая почта").fill(`${who}@marketmesh.test`);
  await page
    .getByLabel("Пароль")
    .fill(
      who === "employee" ? fixture.employeePassword : fixture.otherPassword,
    );
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(page).toHaveURL(/^https:\/\/staff\.localhost:18444\/staff/);
}
async function api(page: Page, method: string, data = {}) {
  return page.evaluate(
    async ({ method, data }) => {
      const reply = await fetch(`/staff.v1.StaffService/${method}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Connect-Protocol-Version": "1",
        },
        body: JSON.stringify(data),
      });
      return { status: reply.status, body: await reply.json() };
    },
    { method, data },
  );
}
async function accessibleInBothThemes(page: Page) {
  for (const theme of ["light", "dark"]) {
    await page.evaluate((theme) => {
      document.documentElement.dataset.theme = theme;
    }, theme);
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    expect(
      await page.evaluate(
        () => getComputedStyle(document.documentElement).colorScheme,
      ),
    ).toBe(theme);
  }
}

test("SSO returns to a one-use invite, isolates cookies, and revokes the previous session", async ({
  page,
  context,
}) => {
  const requests: string[] = [];
  page.on("request", (request) => requests.push(request.url()));
  await page.goto(fixture.active);
  await expect(
    page.getByRole("heading", { name: "Подтвердите рабочую почту." }),
  ).toBeVisible();
  await accessibleInBothThemes(page);
  await login(page);
  await expect(
    page.getByRole("heading", { name: "Вас пригласили в портал." }),
  ).toBeVisible();
  await accessibleInBothThemes(page);
  await page.getByRole("button", { name: "Принять приглашение" }).click();
  await expect(
    page.getByRole("heading", { name: "Доступ открыт." }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Открыть портал" }).click();
  await expect(page.getByText("Доступ к порталу подтверждён.")).toBeVisible();
  expect((await api(page, "GetSession")).body.role).toBe("support");
  await accessibleInBothThemes(page);
  const cookie = (await context.cookies()).find(
    (c) => c.name === "__Host-mm-staff-session",
  )!;
  expect(cookie).toMatchObject({
    domain: "staff.localhost",
    path: "/",
    secure: true,
    httpOnly: true,
    sameSite: "Strict",
  });
  expect(await page.evaluate(() => document.cookie)).not.toContain(
    cookie.value,
  );
  expect(await context.cookies("https://localhost:18443")).toEqual([]);
  expect(
    await context.cookies("https://oidc.localhost:18445"),
  ).not.toContainEqual(expect.objectContaining({ name: cookie.name }));
  await page.goto(fixture.active);
  await expect(
    page.getByRole("heading", { name: "Приглашение уже использовано." }),
  ).toBeVisible();
  const otherTab = await context.newPage();
  await otherTab.goto("/staff");
  await expect(otherTab.getByText("employee@marketmesh.test")).toBeVisible();
  let releaseLogin!: () => void;
  const heldLogin = new Promise<void>((resolve) => {
    releaseLogin = resolve;
  });
  let loginRequested = false;
  await otherTab.route("**/assets/StaffLoginView-*.js", async (route) => {
    loginRequested = true;
    await heldLogin;
    await route.continue();
  });
  await page.goto("/staff/login");
  await login(page);
  await expect.poll(() => loginRequested).toBe(true);
  await expect(
    otherTab.getByText("employee@marketmesh.test"),
  ).not.toBeVisible();
  await expect(
    otherTab.getByRole("heading", { name: "Сессия завершена." }),
  ).toBeVisible();
  releaseLogin();
  await expect(otherTab).toHaveURL(/\/staff\/login\?expired=1$/);
  await expect(
    otherTab.getByText("employee@marketmesh.test"),
  ).not.toBeVisible();
  await otherTab.close();
  await expect(page.getByText("Доступ к порталу подтверждён.")).toBeVisible();
  // An independent client replays the previous opaque cookie after another sign-in.
  const old = await context.request.post("/staff.v1.StaffService/GetSession", {
    headers: {
      Origin: "https://staff.localhost:18444",
      Cookie: `${cookie.name}=${cookie.value}`,
      "Content-Type": "application/json",
    },
    data: {},
  });
  expect(old.status()).toBe(401);
  expect(requests.filter((url) => /\/(auth|user)\.v1\./.test(url))).toEqual([]);
  await page
    .getByRole("button", { name: "Выйти из рабочего аккаунта" })
    .click();
  await expect(page).toHaveURL(/\/staff\/login$/);
  expect((await api(page, "GetSession")).status).toBe(401);
});

test("wrong account cannot consume an invite; expired invite remains closed", async ({
  page,
}) => {
  await page.goto(fixture.wrong);
  await login(page, "other");
  await expect(
    page.getByRole("heading", { name: "Приглашение для другой почты." }),
  ).toBeVisible();
  await accessibleInBothThemes(page);
  const token = new URLSearchParams(new URL(fixture.wrong).hash.slice(1)).get(
    "token",
  );
  expect((await api(page, "AcceptInvite", { inviteToken: token })).status).toBe(
    400,
  );
  await page.getByRole("button", { name: "Сменить рабочий аккаунт" }).click();
  await expect(page).toHaveURL(/^https:\/\/oidc\.localhost:18445/);
  await page.getByLabel("Рабочая почта").fill("employee@marketmesh.test");
  await page.getByLabel("Пароль").fill(fixture.employeePassword);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Вас пригласили в портал." }),
  ).toBeVisible();
  await page.goto(fixture.expired);
  await expect(
    page.getByRole("heading", { name: "Срок приглашения истёк." }),
  ).toBeVisible();
  await accessibleInBothThemes(page);
  await api(page, "Logout");
  await page.goto("/staff");
  await expect(page).toHaveURL(/\/staff\/login\?expired=1$/);
  await expect(page.getByText("Сессия завершена.")).toBeVisible();
});

test("public origin rejects staff assets and RPCs; forged headers do not grant access", async ({
  page,
  context,
}) => {
  for (const path of [
    "/staff",
    "/staff/invite",
    "/staff.v1.StaffService/GetSession",
  ]) {
    const reply = await context.request.get(`https://localhost:18443${path}`, {
      headers: { "X-Forwarded-For": "127.0.0.1", "X-Staff-Subject": "admin" },
    });
    expect(reply.status()).toBe(404);
  }
  await page.goto("/staff/login");
  const unauthenticated = await context.request.post(
    "/staff.v1.StaffService/GetSession",
    {
      headers: {
        Origin: "https://staff.localhost:18444",
        "X-Staff-Subject": "admin",
        Cookie: "access_token=buyer-session",
      },
      data: {},
    },
  );
  expect(unauthenticated.status()).toBe(401);
  const crossOrigin = await context.request.post(
    "/staff.v1.StaffService/StartSso",
    { headers: { Origin: "https://localhost:18443" }, data: {} },
  );
  expect(crossOrigin.status()).toBe(403);
  await accessibleInBothThemes(page);
});
