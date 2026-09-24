import { defineConfig, devices } from '@playwright/test';

// Backend (client-refresh, Task 5+6) поднимается СНАРУЖИ (docker compose или
// go run) — здесь нет webServer, только baseURL из BASE_URL (дефолт
// http://localhost:8085, порт client-refresh в docker-compose.yml).
export default defineConfig({
  testDir: '.',
  fullyParallel: false,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:8085',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
