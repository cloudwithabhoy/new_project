// Vanilla JS SPA, no build step. Talks to the api-gateway directly from the
// browser; the gateway base URL is fetched at load time from GET /config so it
// is never hardcoded. Flow: a login/register gate, then a storefront.
//
// Gateway routes used (6-service scope — no cart):
//   POST /api/auth/register, POST /api/auth/login   (public)
//   GET  /api/products                              (public)
//   POST /api/orders, GET /api/orders?user_id=      (Bearer required)
//
// Ordering submits the line items directly ({user_id, items:[...]}). JWT in localStorage.

(() => {
  'use strict';

  let GATEWAY = '';
  let MODE = 'login'; // 'login' | 'register'
  let PRODUCTS = []; // last loaded product list (for client-side search)
  const TOKEN_KEY = 'shop.jwt';
  const $ = (id) => document.getElementById(id);

  function setStatus(msg, isError = false) {
    const el = $('status');
    el.textContent = msg || '';
    el.className = 'status' + (isError ? ' error' : '') + (msg ? ' show' : '');
  }

  // --- token / session ------------------------------------------------------
  const getToken = () => localStorage.getItem(TOKEN_KEY);
  const setToken = (t) => localStorage.setItem(TOKEN_KEY, t);
  const clearToken = () => localStorage.removeItem(TOKEN_KEY);

  function userIdFromToken() {
    const t = getToken();
    if (!t) return null;
    try {
      const p = JSON.parse(atob(t.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')));
      return p.sub ?? null;
    } catch {
      return null;
    }
  }

  // --- view switching (the login gate) --------------------------------------
  function showApp() {
    $('auth-view').hidden = true;
    $('app-view').hidden = false;
    $('nav-search').hidden = false;
    $('whoami').textContent = `Hi, user ${userIdFromToken()}`;
    $('logout-btn').hidden = false;
  }
  function showAuth() {
    $('app-view').hidden = true;
    $('auth-view').hidden = false;
    $('nav-search').hidden = true;
    $('whoami').textContent = 'not signed in';
    $('logout-btn').hidden = true;
  }

  // --- fetch helper ---------------------------------------------------------
  async function api(path, { method = 'GET', body, auth = false } = {}) {
    const headers = { 'Content-Type': 'application/json' };
    if (auth) {
      const t = getToken();
      if (!t) throw new Error('please log in first');
      headers['Authorization'] = `Bearer ${t}`;
    }
    const res = await fetch(`${GATEWAY}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
    const text = await res.text();
    let data = null;
    if (text) {
      try { data = JSON.parse(text); } catch { data = text; }
    }
    if (!res.ok) {
      const msg = (data && data.error) || `${res.status} ${res.statusText}`;
      throw new Error(msg);
    }
    return data;
  }

  const money = (cents) => (typeof cents === 'number' ? `$${(cents / 100).toFixed(2)}` : '—');

  // --- auth -----------------------------------------------------------------
  function setMode(mode) {
    MODE = mode;
    $('tab-login').classList.toggle('active', mode === 'login');
    $('tab-register').classList.toggle('active', mode === 'register');
    $('fullname-field').hidden = mode !== 'register';
    $('submit-btn').textContent = mode === 'register' ? 'Create account' : 'Log in';
    setStatus('');
  }

  async function submitAuth() {
    const email = $('email').value.trim();
    const password = $('password').value;
    if (!email || !password) return setStatus('email + password required', true);

    if (MODE === 'register') {
      try {
        await api('/api/auth/register', {
          method: 'POST',
          body: { email, password, full_name: $('fullname').value.trim() },
        });
        setStatus('account created — signing you in…');
      } catch (e) {
        return setStatus(`register failed: ${e.message}`, true);
      }
    }
    try {
      const data = await api('/api/auth/login', { method: 'POST', body: { email, password } });
      if (!data || !data.access_token) throw new Error('no token in response');
      setToken(data.access_token);
      setStatus('');
      showApp();
      await Promise.all([loadProducts(), loadOrders()]);
    } catch (e) {
      setStatus(`login failed: ${e.message}`, true);
    }
  }

  function logout() {
    clearToken();
    PRODUCTS = [];
    $('products').innerHTML = '';
    $('orders').innerHTML = '';
    if ($('search')) $('search').value = '';
    showAuth();
    setStatus('logged out');
  }

  // --- products + search ----------------------------------------------------
  async function loadProducts() {
    try {
      const products = await api('/api/products');
      PRODUCTS = Array.isArray(products) ? products : products?.items ?? [];
      renderProducts();
    } catch (e) {
      setStatus(`products failed: ${e.message}`, true);
    }
  }

  // Deterministic pastel colour per product name (for the image tile).
  function thumbStyle(name) {
    let h = 0;
    for (const c of String(name)) h = (h * 31 + c.charCodeAt(0)) % 360;
    return `background:linear-gradient(135deg, hsl(${h} 70% 55%), hsl(${(h + 40) % 360} 70% 45%));`;
  }

  function renderProducts() {
    const q = ($('search')?.value || '').trim().toLowerCase();
    const list = q ? PRODUCTS.filter((p) => (p.name || '').toLowerCase().includes(q)) : PRODUCTS;
    const ul = $('products');
    ul.innerHTML = '';
    if (!list.length) {
      ul.innerHTML = `<li class="empty">${q ? 'No products match your search.' : 'No products yet.'}</li>`;
      return;
    }
    for (const p of list) {
      const name = p.name ?? `product ${p.id}`;
      const stock = p.stock ?? 0;
      const li = document.createElement('li');
      li.className = 'product-card';
      li.innerHTML = `
        <div class="product-thumb" style="${thumbStyle(name)}">${escapeHtml(name[0] || '?')}</div>
        <div class="product-name">${escapeHtml(name)}</div>
        <div class="product-price">${money(p.price_cents)}</div>
        <div class="product-stock${stock > 0 ? '' : ' out'}">${stock > 0 ? `In stock (${stock})` : 'Out of stock'}</div>
        <div class="product-buy">
          <input type="number" min="1" value="1" class="qty" aria-label="quantity" />
          <button class="btn-buy" type="button">Buy now</button>
        </div>`;
      const qty = li.querySelector('.qty');
      li.querySelector('.btn-buy').addEventListener('click', () =>
        placeOrder(p, Math.max(1, Number(qty.value) || 1))
      );
      ul.appendChild(li);
    }
  }

  // --- orders (direct line items — no cart) ---------------------------------
  async function placeOrder(p, quantity) {
    const uid = userIdFromToken();
    if (!uid) return setStatus('please log in first', true);
    try {
      const order = await api('/api/orders', {
        method: 'POST',
        auth: true,
        body: {
          user_id: Number(uid) || uid,
          items: [{ product_id: p.id, name: p.name, price_cents: p.price_cents, quantity }],
        },
      });
      setStatus(`Order #${order?.id ?? '?'} placed — ${money(order?.total_cents)} (${order?.status ?? 'ok'})`);
      await loadOrders();
    } catch (e) {
      setStatus(`order failed: ${e.message}`, true);
    }
  }

  async function loadOrders() {
    const uid = userIdFromToken();
    if (!uid) return;
    try {
      const orders = await api(`/api/orders?user_id=${encodeURIComponent(uid)}`, { auth: true });
      const list = Array.isArray(orders) ? orders : orders?.items ?? [];
      const ul = $('orders');
      ul.innerHTML = '';
      if (!list.length) {
        ul.innerHTML = '<li class="empty">No orders yet — buy something above.</li>';
        return;
      }
      for (const o of list) {
        const li = document.createElement('li');
        li.className = 'order-row';
        li.innerHTML = `
          <span class="order-id">Order #${o.id}</span>
          <span class="order-meta">
            <span class="order-total">${money(o.total_cents)}</span>
            <span class="pill">${escapeHtml(o.status ?? '')}</span>
          </span>`;
        ul.appendChild(li);
      }
    } catch (e) {
      setStatus(`orders failed: ${e.message}`, true);
    }
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    })[c]);
  }

  // --- boot -----------------------------------------------------------------
  async function init() {
    try {
      const cfg = await (await fetch('/config')).json();
      GATEWAY = cfg.apiGatewayUrl;
      $('gateway-url').textContent = GATEWAY;
    } catch (e) {
      setStatus(`could not load /config: ${e.message}`, true);
      return;
    }

    $('tab-login').addEventListener('click', () => setMode('login'));
    $('tab-register').addEventListener('click', () => setMode('register'));
    $('submit-btn').addEventListener('click', submitAuth);
    $('logout-btn').addEventListener('click', logout);
    $('refresh-products').addEventListener('click', loadProducts);
    $('refresh-orders').addEventListener('click', loadOrders);
    $('search').addEventListener('input', renderProducts);
    $('password').addEventListener('keydown', (e) => { if (e.key === 'Enter') submitAuth(); });

    if (getToken()) {
      showApp();
      await Promise.all([loadProducts(), loadOrders()]);
    } else {
      showAuth();
    }
  }

  init();
})();
