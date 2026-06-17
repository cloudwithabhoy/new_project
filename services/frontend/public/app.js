// Vanilla JS SPA. No build step. Talks to the api-gateway directly from the
// browser. The gateway base URL is fetched at load time from GET /config so it
// is never hardcoded into the page.
//
// Gateway routes used (api-details §3 api-gateway):
//   POST /api/auth/register, /api/auth/login   (public)
//   GET  /api/products                          (public)
//   GET/POST /api/cart/{user_id}[/items]        (Bearer required)
//   GET/POST /api/orders                        (Bearer required)
//
// JWT is stored in localStorage. The user_id comes from the JWT `sub` claim.

(() => {
  'use strict';

  let GATEWAY = '';
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

  // Decode the `sub` (user_id) from a JWT without verifying (display/routing
  // only; the gateway does the real verification).
  function userIdFromToken() {
    const t = getToken();
    if (!t) return null;
    try {
      const payload = JSON.parse(
        atob(t.split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))
      );
      return payload.sub ?? null;
    } catch {
      return null;
    }
  }

  function refreshSession() {
    const uid = userIdFromToken();
    if (uid) {
      $('whoami').textContent = `signed in (user ${uid})`;
      $('logout-btn').hidden = false;
    } else {
      $('whoami').textContent = 'not signed in';
      $('logout-btn').hidden = true;
    }
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
      try {
        data = JSON.parse(text);
      } catch {
        data = text;
      }
    }
    if (!res.ok) {
      const msg = (data && data.error) || `${res.status} ${res.statusText}`;
      throw new Error(msg);
    }
    return data;
  }

  const money = (cents) =>
    typeof cents === 'number' ? `$${(cents / 100).toFixed(2)}` : '—';

  // --- auth -----------------------------------------------------------------
  async function register() {
    const email = $('email').value.trim();
    const password = $('password').value;
    const full_name = $('fullname').value.trim();
    if (!email || !password) return setStatus('email + password required', true);
    try {
      await api('/api/auth/register', {
        method: 'POST',
        body: { email, password, full_name },
      });
      setStatus('registered — now log in');
    } catch (e) {
      setStatus(`register failed: ${e.message}`, true);
    }
  }

  async function login() {
    const email = $('email').value.trim();
    const password = $('password').value;
    if (!email || !password) return setStatus('email + password required', true);
    try {
      const data = await api('/api/auth/login', {
        method: 'POST',
        body: { email, password },
      });
      if (!data || !data.access_token) throw new Error('no token in response');
      setToken(data.access_token);
      refreshSession();
      setStatus('logged in');
      await Promise.all([loadCart(), loadOrders()]);
    } catch (e) {
      setStatus(`login failed: ${e.message}`, true);
    }
  }

  function logout() {
    clearToken();
    refreshSession();
    $('cart').innerHTML = '';
    $('cart-total').textContent = '—';
    $('orders').innerHTML = '';
    setStatus('logged out');
  }

  // --- products -------------------------------------------------------------
  async function loadProducts() {
    try {
      const products = await api('/api/products');
      const list = Array.isArray(products) ? products : products?.items ?? [];
      const ul = $('products');
      ul.innerHTML = '';
      if (!list.length) {
        ul.innerHTML = '<li class="muted">no products</li>';
        return;
      }
      for (const p of list) {
        const li = document.createElement('li');
        li.innerHTML = `
          <div>
            <strong>${escapeHtml(p.name ?? `product ${p.id}`)}</strong>
            <span class="muted">${money(p.price_cents)} · stock ${p.stock ?? '?'}</span>
          </div>`;
        const btn = document.createElement('button');
        btn.textContent = 'add to cart';
        btn.addEventListener('click', () => addToCart(p.id));
        li.appendChild(btn);
        ul.appendChild(li);
      }
    } catch (e) {
      setStatus(`products failed: ${e.message}`, true);
    }
  }

  // --- cart -----------------------------------------------------------------
  async function addToCart(productId) {
    const uid = userIdFromToken();
    if (!uid) return setStatus('please log in first', true);
    try {
      await api(`/api/cart/${uid}/items`, {
        method: 'POST',
        auth: true,
        body: { product_id: productId, quantity: 1 },
      });
      setStatus('added to cart');
      await loadCart();
    } catch (e) {
      setStatus(`add to cart failed: ${e.message}`, true);
    }
  }

  async function loadCart() {
    const uid = userIdFromToken();
    if (!uid) return;
    try {
      const cart = await api(`/api/cart/${uid}`, { auth: true });
      renderCart(cart);
    } catch (e) {
      setStatus(`cart failed: ${e.message}`, true);
    }
  }

  function renderCart(cart) {
    const ul = $('cart');
    ul.innerHTML = '';
    const items = cart?.items ?? [];
    if (!items.length) {
      ul.innerHTML = '<li class="muted">cart is empty</li>';
    }
    for (const it of items) {
      const li = document.createElement('li');
      li.innerHTML = `<div>
          <strong>${escapeHtml(it.name ?? `product ${it.product_id}`)}</strong>
          <span class="muted">${money(it.price_cents)} × ${it.quantity}</span>
        </div>`;
      ul.appendChild(li);
    }
    $('cart-total').textContent = money(cart?.total_cents);
  }

  // --- orders ---------------------------------------------------------------
  async function checkout() {
    const uid = userIdFromToken();
    if (!uid) return setStatus('please log in first', true);
    try {
      const order = await api('/api/orders', {
        method: 'POST',
        auth: true,
        body: { user_id: Number(uid) || uid },
      });
      setStatus(`order #${order?.id ?? '?'} placed (${order?.status ?? 'ok'})`);
      await Promise.all([loadCart(), loadOrders()]);
    } catch (e) {
      setStatus(`checkout failed: ${e.message}`, true);
    }
  }

  async function loadOrders() {
    const uid = userIdFromToken();
    if (!uid) return;
    try {
      const orders = await api(`/api/orders?user_id=${encodeURIComponent(uid)}`, {
        auth: true,
      });
      const list = Array.isArray(orders) ? orders : orders?.items ?? [];
      const ul = $('orders');
      ul.innerHTML = '';
      if (!list.length) {
        ul.innerHTML = '<li class="muted">no orders</li>';
        return;
      }
      for (const o of list) {
        const li = document.createElement('li');
        li.innerHTML = `<div>
            <strong>order #${o.id}</strong>
            <span class="muted">${money(o.total_cents)} · ${escapeHtml(o.status ?? '')}</span>
          </div>`;
        ul.appendChild(li);
      }
    } catch (e) {
      setStatus(`orders failed: ${e.message}`, true);
    }
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;',
      '<': '&lt;',
      '>': '&gt;',
      '"': '&quot;',
      "'": '&#39;',
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

    $('register-btn').addEventListener('click', register);
    $('login-btn').addEventListener('click', login);
    $('logout-btn').addEventListener('click', logout);
    $('refresh-products').addEventListener('click', loadProducts);
    $('refresh-cart').addEventListener('click', loadCart);
    $('refresh-orders').addEventListener('click', loadOrders);
    $('checkout-btn').addEventListener('click', checkout);

    refreshSession();
    await loadProducts();
    if (getToken()) {
      await Promise.all([loadCart(), loadOrders()]);
    }
  }

  init();
})();
