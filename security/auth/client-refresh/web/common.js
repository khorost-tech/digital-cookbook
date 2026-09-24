// common.js — общие хелперы для naive.js/coordinated.js (demo client-refresh,
// ст. 5): работа со статусом на странице (источник истины для Playwright —
// body[data-loggedout]) и низкоуровневые запросы к /auth/login, /auth/refresh.
// Никакой координации между вкладками здесь нет — это чисто транспортный
// и UI-слой, общий для обеих реализаций.

export function setLoggedIn(loggedIn) {
  document.body.dataset.loggedout = loggedIn ? 'false' : 'true';
  const el = document.getElementById('status');
  if (el) el.textContent = loggedIn ? 'logged-in' : 'logged-out';
}

export function log(msg) {
  const line = `[${new Date().toISOString().slice(11, 23)}] ${msg}`;
  console.log(line);
  const el = document.getElementById('log');
  if (el) el.textContent += line + '\n';
}

// login — demo-логин: email не важен для замера (backend детерминированно
// отображает email в Account), важен сам факт живой refresh/access cookie,
// общей для всех вкладок одного browser context (одна вкладка логинится —
// остальные видят ту же сессию после reload).
export async function login() {
  const email = `demo-${Date.now()}@example.test`;
  const res = await fetch('/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
    credentials: 'same-origin',
  });
  if (!res.ok) {
    log(`login failed: ${res.status}`);
    setLoggedIn(false);
    return null;
  }
  const body = await res.json();
  setLoggedIn(true);
  log('login ok');
  return body.access_token;
}

// rawRefresh — один сетевой POST /auth/refresh без какой-либо клиентской
// координации. refresh-cookie (httpOnly, Path=/auth) прикладывается браузером
// автоматически. Возвращает { ok, status, accessToken? }.
export async function rawRefresh() {
  const res = await fetch('/auth/refresh', {
    method: 'POST',
    credentials: 'same-origin',
  });
  if (!res.ok) {
    return { ok: false, status: res.status };
  }
  const body = await res.json();
  return { ok: true, status: res.status, accessToken: body.access_token };
}
