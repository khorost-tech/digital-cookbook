// naive.js — наивный клиент client-refresh demo (ст. 5): воспроизводит
// ложные разлогины под гонкой нескольких вкладок с общей refresh-cookie.
//
// При клике #force-refresh вкладка САМА, без какой-либо координации с
// другими вкладками того же браузера, шлёт POST /auth/refresh. Backend
// (Task 5, Rotate) держит распределённый single-flight lock на аккаунт:
// ровно одна из параллельных вкладок выигрывает лок и ротирует токен,
// остальные получают 429 refresh_in_progress (см.
// client-refresh/rotation.go: ErrRefreshInProgress). Наивный клиент
// трактует ЛЮБОЙ не-200 ответ /auth/refresh (429 ИЛИ 401) как разлогин —
// не ретраит, не координируется. Это и есть баг, который устраняет
// coordinated.js (BroadcastChannel + Web Locks single-flight на клиенте).
import { setLoggedIn, log, login, rawRefresh } from './common.js';

export function init() {
  document.getElementById('login').addEventListener('click', () => {
    login();
  });

  document.getElementById('force-refresh').addEventListener('click', async () => {
    log('naive: force-refresh clicked');
    const result = await rawRefresh();
    if (!result.ok) {
      // Наивная обработка: 429 (проигранный backend-lock) неотличим от 401
      // (реальный разлогин) — оба трактуются как "меня разлогинили".
      log(`naive: refresh failed (status=${result.status}) -> treated as logout`);
      setLoggedIn(false);
      return;
    }
    log('naive: refresh ok');
    setLoggedIn(true);
  });
}
