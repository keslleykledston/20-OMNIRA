import { test, expect, Page } from '@playwright/test';

// Minimal responsive smoke for the Inbox workspace — proves layout behavior at
// the gate viewports, not a full visual regression suite. Reuses the same
// dev-mode login and seeded fixture as inbox.spec.ts (scripts/e2e-inbox.sh).
const TENANT = '11111111-1111-1111-1111-111111111111';
const AGENT = { email: 'test@omnira.local' };

async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('E-mail').fill(email);
  await page.getByRole('button', { name: /Entrar/ }).click();
  await page.waitForURL('/inbox', { timeout: 10_000 });
}

const viewports = [
  { name: '1440 (desktop)', width: 1440, height: 1024 },
  { name: '1024 (lg breakpoint)', width: 1024, height: 900 },
  { name: '768 (tablet)', width: 768, height: 1024 },
  { name: '390 (mobile)', width: 390, height: 844 },
];

for (const vp of viewports) {
  test(`inbox workspace at ${vp.name}: no horizontal overflow, correct panel mode`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await login(page, AGENT.email);
    await page.goto('/inbox');

    const row = page.getByRole('heading', { level: 4, name: 'Maria Souza' });
    const chatHeader = page.getByRole('heading', { level: 3, name: 'Maria Souza' });
    const isDesktop = vp.width >= 1024; // Tailwind's `lg:` breakpoint

    if (isDesktop) {
      // Desktop/lg+: list + chat (+ context) all visible at once — never compressed to one panel.
      await expect(row).toBeVisible();
      await expect(chatHeader).toBeVisible();
    } else {
      // Below lg: single-panel navigation stack. InboxWorkspace auto-selects the
      // first conversation, so the chat shows immediately and the list column
      // gets Tailwind's `hidden` (display:none — removed from the a11y tree
      // entirely, not just visually hidden, hence checking for the row here
      // would report "not found"). Verify the chat is what actually shows,
      // then that Back genuinely returns to the list.
      await expect(chatHeader).toBeVisible();
      await expect(row).toBeHidden();
      await page.getByTitle('Voltar').click();
      await expect(row).toBeVisible();
      await expect(chatHeader).toBeHidden();
    }

    // No horizontal scroll at any breakpoint (viewport width == document scrollWidth).
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1); // 1px tolerance for scrollbar rounding
  });
}
