import { defineConfig, devices } from '@playwright/test';

// Оба бэкенда (webauthn-go :8087, webauthn-java :8088, Task 4/5) поднимаются
// СНАРУЖИ (docker compose) — здесь нет webServer. Каждый тест сам открывает
// нужный BASE_GO/BASE_JAVA (см. webauthn.spec.ts), поэтому общий baseURL не
// задаём.
export default defineConfig({
  testDir: '.',
  fullyParallel: false,
  retries: 0,
  reporter: [['list']],
  use: {
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
