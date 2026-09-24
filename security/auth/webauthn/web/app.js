// Ванильный JS без сборки: navigator.credentials.create()/.get() против одного
// из двух бэкендов (Go :8087, Java :8088), выбираемого через ?backend=go|java.
// Оба бэкенда отдают эту же страницу сами (одинаковый origin у вызывающего
// fetch() и у ceremony), поэтому переключатель влияет только на BASE — он не
// нужен для CORS, только для явного документирования "с кем говорим".
//
// JSON-контракт обоих бэкендов сверен вручную (Task 6): имена полей
// challenge/user.id/rp/pubKeyCredParams/excludeCredentials/allowCredentials/
// timeout, а также форма register/finish и login/finish тел (id/rawId/type/
// response.{clientDataJSON,attestationObject|authenticatorData+signature})
// совпадают у Go (go-webauthn) и Java (webauthn4j) бэкендов — везде
// base64url без паддинга. Различаются только необязательные поля
// (authenticatorSelection/attestation/полнота pubKeyCredParams), которые не
// требуют разного кода на фронтенде: navigator.credentials.create()/.get()
// сами подставляют дефолты для отсутствующих необязательных полей.

const BACKENDS = {
  go: 'http://localhost:8087',
  java: 'http://localhost:8088',
};

const params = new URLSearchParams(location.search);
const backendName = params.get('backend') in BACKENDS ? params.get('backend') : null;
const BASE = backendName ? BACKENDS[backendName] : location.origin;

const els = {
  backend: document.getElementById('backend'),
  username: document.getElementById('username'),
  register: document.getElementById('register'),
  login: document.getElementById('login'),
  status: document.getElementById('status'),
  log: document.getElementById('log'),
};

els.backend.textContent = `${backendName || '(default)'} → ${BASE}`;

function logLine(msg) {
  els.log.textContent += `${msg}\n`;
  els.log.scrollTop = els.log.scrollHeight;
}

function setStatus(text, authok) {
  els.status.textContent = text;
  if (authok !== undefined) {
    document.body.setAttribute('data-authok', authok);
  }
}

// ---- base64url helpers ----------------------------------------------------
// Оба бэкенда используют base64url БЕЗ паддинга (RawURLEncoding в Go,
// Base64.getUrlEncoder().withoutPadding() в Java) — декодер терпим к
// отсутствию паддинга, энкодер паддинг не добавляет.

function base64urlToBuffer(base64url) {
  const padded = base64url.replace(/-/g, '+').replace(/_/g, '/');
  const pad = padded.length % 4 === 0 ? '' : '='.repeat(4 - (padded.length % 4));
  const raw = atob(padded + pad);
  const buf = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) buf[i] = raw.charCodeAt(i);
  return buf.buffer;
}

function bufferToBase64url(buffer) {
  const bytes = new Uint8Array(buffer);
  let str = '';
  for (const b of bytes) str += String.fromCharCode(b);
  return btoa(str).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// ---- ceremony option decoding ----------------------------------------------

function decodeCreationOptions(publicKey) {
  const opts = { ...publicKey };
  opts.challenge = base64urlToBuffer(publicKey.challenge);
  opts.user = { ...publicKey.user, id: base64urlToBuffer(publicKey.user.id) };
  if (publicKey.excludeCredentials) {
    opts.excludeCredentials = publicKey.excludeCredentials.map((c) => ({
      ...c,
      id: base64urlToBuffer(c.id),
    }));
  }
  return opts;
}

function decodeRequestOptions(publicKey) {
  const opts = { ...publicKey };
  opts.challenge = base64urlToBuffer(publicKey.challenge);
  if (publicKey.allowCredentials) {
    opts.allowCredentials = publicKey.allowCredentials.map((c) => ({
      ...c,
      id: base64urlToBuffer(c.id),
    }));
  }
  return opts;
}

async function postJSON(path, body) {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let data;
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { error: text };
  }
  if (!res.ok) {
    throw new Error(data.error || `HTTP ${res.status}`);
  }
  return data;
}

// ---- registration -----------------------------------------------------------

async function register() {
  const username = els.username.value.trim();
  if (!username) {
    setStatus('username required', 'failed');
    return;
  }
  try {
    setStatus('register: begin…');
    const begin = await postJSON('/register/begin', { username });
    logLine(`register/begin ok: ${JSON.stringify(begin)}`);

    const publicKey = decodeCreationOptions(begin.publicKey);
    setStatus('register: navigator.credentials.create()…');
    const credential = await navigator.credentials.create({ publicKey });

    const body = {
      id: credential.id,
      rawId: bufferToBase64url(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: bufferToBase64url(credential.response.clientDataJSON),
        attestationObject: bufferToBase64url(credential.response.attestationObject),
      },
    };

    await postJSON(`/register/finish?username=${encodeURIComponent(username)}`, body);
    logLine('register/finish ok');
    setStatus('registered', 'registered');
  } catch (err) {
    logLine(`register failed: ${err.message || err}`);
    setStatus(`register failed: ${err.message || err}`, 'failed');
  }
}

// ---- login --------------------------------------------------------------

async function login() {
  const username = els.username.value.trim();
  if (!username) {
    setStatus('username required', 'failed');
    return;
  }
  try {
    setStatus('login: begin…');
    const begin = await postJSON('/login/begin', { username });
    logLine(`login/begin ok: ${JSON.stringify(begin)}`);

    const publicKey = decodeRequestOptions(begin.publicKey);
    setStatus('login: navigator.credentials.get()…');
    const credential = await navigator.credentials.get({ publicKey });

    const body = {
      id: credential.id,
      rawId: bufferToBase64url(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: bufferToBase64url(credential.response.clientDataJSON),
        authenticatorData: bufferToBase64url(credential.response.authenticatorData),
        signature: bufferToBase64url(credential.response.signature),
      },
    };

    await postJSON(`/login/finish?username=${encodeURIComponent(username)}`, body);
    logLine('login/finish ok');
    setStatus('logged-in', 'logged-in');
  } catch (err) {
    logLine(`login failed: ${err.message || err}`);
    setStatus(`login failed: ${err.message || err}`, 'failed');
  }
}

els.register.addEventListener('click', register);
els.login.addEventListener('click', login);
