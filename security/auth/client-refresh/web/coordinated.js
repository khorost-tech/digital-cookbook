// coordinated.js — координированный клиент client-refresh demo (ст. 5):
// устраняет ложные разлогины под гонкой нескольких вкладок с общей
// refresh-cookie, которую воспроизводит naive.js.
//
// Механизм:
//  1. navigator.locks.request('auth-refresh', ...) — Web Locks scoped на
//     storage partition, т.е. single-flight В РАМКАХ БРАУЗЕРА: все вкладки
//     одного origin разделяют один LockManager. Только держатель лока шлёт
//     сетевой POST /auth/refresh; остальные вкладки встают в очередь Web
//     Locks и ждут своей очереди, НЕ отправляя параллельный запрос —
//     backend refresh-lock (client-refresh/rotation.go, rl:{accountID})
//     никогда не видит конкурентных запросов ОТ ЭТОГО БРАУЗЕРА, поэтому
//     429 не возникает в принципе (запросы сериализованы клиентом, а
//     сериализованный refresh backend обслуживает всегда 200 — либо
//     свежая ротация, либо replay уже существующего преемника по grace-
//     мосту, см. Rotate()).
//  2. Пока вкладка ждёт лок, другая вкладка могла уже выполнить refresh и
//     разослать результат через BroadcastChannel('auth'). Проверяем
//     freshness (lastRefreshAt) сразу после получения лока: если refresh
//     уже случился недавно — используем его результат и НЕ шлём повторный
//     сетевой запрос вовсе (чистая оптимизация: даже без неё повторный
//     запрос под локом был бы сериализован и получил бы 200, см. п.1 —
//     но лишний round-trip к backend ни к чему).
//  3. BroadcastChannel('auth'): 'refreshed' -> все вкладки обновляют access
//     в памяти и остаются logged-in; 'logout' -> все вкладки logged-out
//     (настоящий разлогин: backend вернул 401/ErrTokenInvalid — сессии и
//     grace-моста уже нет ни для кого).
//  4. Фолбэк на 'storage'-событие (для браузеров без BroadcastChannel, как
//     резервный кросс-таб канал) в этом demo сознательно НЕ реализован —
//     оставлен только как комментарий; в продакшн-клиенте это была бы
//     дополнительная ветка синхронизации.
import { setLoggedIn, log, login, rawRefresh } from './common.js';

const CHANNEL_NAME = 'auth';
const LOCK_NAME = 'auth-refresh';
// Если refresh (в этой или другой вкладке) случился менее FRESH_WINDOW_MS
// назад к моменту получения лока — считаем результат актуальным и не шлём
// повторный сетевой запрос.
const FRESH_WINDOW_MS = 2000;

let accessToken = null;
let lastRefreshAt = 0;
let channel = null;

function getChannel() {
  if (channel || !('BroadcastChannel' in window)) return channel;

  channel = new BroadcastChannel(CHANNEL_NAME);
  channel.onmessage = (ev) => {
    const msg = ev.data;
    if (!msg || typeof msg !== 'object') return;
    if (msg.type === 'refreshed') {
      accessToken = msg.accessToken;
      lastRefreshAt = msg.at;
      log('coordinated: got "refreshed" via BroadcastChannel');
      setLoggedIn(true);
    } else if (msg.type === 'logout') {
      accessToken = null;
      log('coordinated: got "logout" via BroadcastChannel');
      setLoggedIn(false);
    }
  };
  return channel;
}

// rawSingleRefresh — фактический сетевой POST /auth/refresh. Вызывается
// ТОЛЬКО держателем Web Lock, поэтому от этого браузера backend никогда не
// видит конкурентных запросов на refresh.
async function rawSingleRefresh() {
  const result = await rawRefresh();
  if (!result.ok) {
    // Единственный, кто реально бьёт по сети, всё равно получил ошибку —
    // например, сессии действительно больше нет (401/ErrTokenInvalid).
    // Это настоящий разлогин, рассылаем его всем вкладкам.
    log(`coordinated: refresh failed (status=${result.status}) -> real logout`);
    accessToken = null;
    setLoggedIn(false);
    getChannel()?.postMessage({ type: 'logout' });
    return;
  }
  accessToken = result.accessToken;
  lastRefreshAt = Date.now();
  log('coordinated: refresh ok, broadcasting');
  setLoggedIn(true);
  getChannel()?.postMessage({ type: 'refreshed', accessToken, at: lastRefreshAt });
}

async function coordinatedRefresh() {
  if (!('locks' in navigator)) {
    // Web Locks недоступны (нестандартный/очень старый браузер) — фолбэк на
    // прямой запрос, как naive.js. В целевых окружениях (Chromium/
    // Playwright) API присутствует.
    log('coordinated: navigator.locks unavailable, falling back to raw refresh');
    await rawSingleRefresh();
    return;
  }

  await navigator.locks.request(LOCK_NAME, async () => {
    if (accessToken && Date.now() - lastRefreshAt < FRESH_WINDOW_MS) {
      log('coordinated: fresh token already available (broadcast beat the lock queue), skip network refresh');
      setLoggedIn(true);
      return;
    }
    await rawSingleRefresh();
  });
}

export function init() {
  getChannel();

  document.getElementById('login').addEventListener('click', async () => {
    accessToken = await login();
    lastRefreshAt = Date.now();
  });

  document.getElementById('force-refresh').addEventListener('click', () => {
    log('coordinated: force-refresh clicked');
    coordinatedRefresh();
  });
}
