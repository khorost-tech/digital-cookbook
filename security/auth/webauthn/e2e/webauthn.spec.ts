import { test, expect } from '@playwright/test';

// Реальный registration→login через CDP virtual authenticator (ctap2,
// resident key, автоматическая user presence/verification) против ОБОИХ
// webauthn-бэкендов (Task 4 Go, Task 5 Java) — один и тот же frontend
// (web/index.html+app.js), т.к. JSON-контракты сверены и совместимы
// (см. комментарий в app.js).

const BASE_GO = process.env.BASE_GO || 'http://localhost:8087';
const BASE_JAVA = process.env.BASE_JAVA || 'http://localhost:8088';

for (const [backend, base] of [['go', BASE_GO], ['java', BASE_JAVA]] as const) {
  test(`register+login via virtual authenticator (${backend})`, async ({ page }) => {
    const client = await page.context().newCDPSession(page);
    await client.send('WebAuthn.enable');
    const { authenticatorId } = await client.send('WebAuthn.addVirtualAuthenticator', {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    });

    await page.goto(`${base}/?backend=${backend}`);
    await page.fill('#username', `demo-${backend}`);

    await page.click('#register');
    await expect(page.locator('body')).toHaveAttribute('data-authok', 'registered', { timeout: 8000 });

    await page.click('#login');
    await expect(page.locator('body')).toHaveAttribute('data-authok', 'logged-in', { timeout: 8000 });

    await client.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId });
  });
}
