import { expect, type Page } from '@playwright/test';

export interface TestLetter {
  ID: string;
  Subject: string;
  Text: string;
  To: { Address: string }[];
}
/** Reads only the disposable fixture's local Mailpit; no public mail account or SMTP relay. */
export async function letter(
  email: string,
  subject: string,
  exclude = new Set<string>(),
): Promise<TestLetter> {
  const base = process.env.MAILPIT_URL;
  if (!['http://mailpit:8025', 'http://localhost:18025'].includes(base ?? ''))
    throw new Error('Local Mailpit fixture required');
  let found: TestLetter | undefined;
  await expect
    .poll(
      async () => {
        const response = await fetch(
          `${base}/api/v1/search?query=${encodeURIComponent(`to:${email}`)}`,
          { signal: AbortSignal.timeout(5000) },
        );
        if (!response.ok) return false;
        const list = (await response.json()) as { messages: { ID: string; Subject: string }[] };
        const match = list.messages.find(
          (item) => item.Subject === subject && !exclude.has(item.ID),
        );
        if (!match) return false;
        const detail = await fetch(`${base}/api/v1/message/${encodeURIComponent(match.ID)}`, {
          signal: AbortSignal.timeout(5000),
        });
        if (!detail.ok) return false;
        const value = (await detail.json()) as TestLetter;
        if (!value.To.some((to) => to.Address === email) || typeof value.Text !== 'string')
          return false;
        found = value;
        return true;
      },
      { timeout: 30_000, message: 'Expected local email delivery' },
    )
    .toBe(true);
  return found!;
}
export function letterLink(mail: TestLetter, action: string): string {
  const match = mail.Text.match(
    /https:\/\/localhost:\d+\/account\/security\/[a-z_]+#token=[A-Za-z0-9_.-]+/g,
  )?.find((url) => new URL(url).pathname === `/account/security/${action}`);
  if (!match) throw new Error('Expected account action link');
  const url = new URL(match);
  // The browser container uses the same fixture through its internal DNS name.
  return url.pathname + url.hash;
}
export function letterCode(mail: TestLetter): string {
  const code = mail.Text.match(/\b\d{6}\b/)?.[0];
  if (!code) throw new Error('Expected six digit code');
  return code;
}
export async function verifyEmail(page: Page, email: string) {
  const mail = await letter(email, 'Подтвердите почту в MarketMesh');
  await page.goto(letterLink(mail, 'verify'));
  await expect.poll(() => new URL(page.url()).hash).toBe('');
  await page.getByRole('button', { name: 'Подтвердить почту', exact: true }).click();
  await expect(page).toHaveURL(/\/account(?:\/id)?$/);
}

/** Each login reads only the newly requested code, never a previous Mailpit letter. */
export async function submitLogin(page: Page, email: string) {
  const base = process.env.MAILPIT_URL;
  if (!['http://mailpit:8025', 'http://localhost:18025'].includes(base ?? ''))
    throw new Error('Local Mailpit fixture required');
  const response = await fetch(`${base}/api/v1/search?query=${encodeURIComponent(`to:${email}`)}`);
  if (!response.ok) throw new Error('Cannot inspect local mail');
  const list = (await response.json()) as { messages: { ID: string }[] };
  const seen = new Set(list.messages.map((message) => message.ID));
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByLabel('Код из письма', { exact: true })).toBeVisible();
  const mail = await letter(email, 'Код для входа в MarketMesh', seen);
  await page.getByLabel('Код из письма', { exact: true }).fill(letterCode(mail));
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
}
