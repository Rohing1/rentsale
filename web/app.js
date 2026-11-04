const state = {
  products: [],
  customers: [],
  orders: [],
  orderItems: new Map(),
  dashboard: null,
};

const currency = new Intl.NumberFormat("zh-CN", { style: "currency", currency: "CNY" });
const dateFormatter = new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" });
const $ = (selector) => document.querySelector(selector);

document.addEventListener("DOMContentLoaded", () => {
  bindEvents();
  setDefaultDates();
  refreshAll();
});

function bindEvents() {
  document.querySelectorAll("[data-view-link]").forEach((element) => {
    element.addEventListener("click", () => switchView(element.dataset.viewLink));
  });
  $("#refresh-button").addEventListener("click", refreshAll);
  $("#api-key").addEventListener("change", refreshAll);
  $("#product-form").addEventListener("submit", createProduct);
  $("#customer-form").addEventListener("submit", createCustomer);
  $("#order-form").addEventListener("submit", createOrder);
  $("#add-order-item").addEventListener("click", addOrderItem);
  $("#order-type").addEventListener("change", toggleRentalFields);
  $("#product-search").addEventListener("input", debounce(() => loadProducts(), 220));
  $("#customer-search").addEventListener("input", debounce(() => loadCustomers(), 220));
  $("#order-type-filter").addEventListener("change", loadOrders);
  $("#order-status-filter").addEventListener("change", loadOrders);
  document.addEventListener("click", handleDelegatedClick);
}

async function refreshAll() {
  await Promise.all([loadDashboard(), loadProducts(), loadCustomers(), loadOrders()]);
}

async function api(path, options = {}) {
  const headers = { Accept: "application/json", ...(options.headers || {}) };
  if (options.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";
  const key = $("#api-key").value.trim();
  if (key) headers["X-API-Key"] = key;
  const response = await fetch(path, { ...options, headers });
  const isJSON = response.headers.get("content-type")?.includes("application/json");
  const data = isJSON ? await response.json() : null;
  if (!response.ok) throw new Error(data?.error?.message || `请求失败（${response.status}）`);
  return data;
}

async function loadDashboard() {
  try {
    state.dashboard = await api("/api/dashboard");
    const metrics = [
      ["在架商品", state.dashboard.active_products, "当前可经营商品数"],
      ["今日可用库存", state.dashboard.available_now, "已扣除今天的租赁占用"],
      ["租赁中", state.dashboard.checked_out_rentals, "已完成取货，尚未归还"],
      ["3 天内待归还", state.dashboard.upcoming_returns, "需要提前提醒与备货"],
      ["今日销售额", money(state.dashboard.today_sales_cents), "已确认或已完成销售单"],
      ["低库存商品", state.dashboard.low_stock_products, "现货数量不高于 2"],
    ];
    $("#dashboard-metrics").innerHTML = metrics.map(([label, value, note]) => `<article class="metric"><span>${label}</span><strong>${value}</strong><small>${note}</small></article>`).join("");
  } catch (error) { showError(error); }
}

async function loadProducts() {
  try {
    const keyword = $("#product-search").value.trim();
    const data = await api(`/api/products?q=${encodeURIComponent(keyword)}`);
    state.products = data.items || [];
    renderProducts();
    renderOrderProductOptions();
  } catch (error) { showError(error); }
}

function renderProducts() {
  const table = $("#product-table");
  $("#product-count").textContent = `共 ${state.products.length} 个在架商品`;
  if (!state.products.length) {
    table.innerHTML = `<tr><td colspan="5" class="empty">还没有商品。先在左侧创建一个商品。</td></tr>`;
    return;
  }
  table.innerHTML = state.products.map((product) => {
    const modes = `${product.rent_enabled ? '<span class="tag rent">可租</span>' : ''}${product.sell_enabled ? '<span class="tag sale">可售</span>' : ''}`;
    return `<tr><td><div class="item-title">${escapeHTML(product.name)}</div><div class="item-subtitle">${escapeHTML(product.sku)} · ${escapeHTML(product.location || "未设置仓点")}</div></td><td><div class="modes">${modes}</div></td><td><div>${product.rent_enabled ? `租 ${money(product.daily_rent_cents)}/天` : "—"}</div><div class="item-subtitle">${product.sell_enabled ? `售 ${money(product.sale_price_cents)}` : "仅租赁"}</div></td><td><strong>${product.stock_on_hand}</strong></td><td><button class="button secondary small" data-stock-product="${product.id}">调库存</button></td></tr>`;
  }).join("");
}

function renderOrderProductOptions() {
  const selected = $("#order-product").value;
  const mode = $("#order-type").value;
  const products = state.products.filter((product) => mode === "rental" ? product.rent_enabled : product.sell_enabled);
  $("#order-product").innerHTML = products.length
    ? products.map((product) => `<option value="${product.id}">${escapeHTML(product.name)} (${escapeHTML(product.sku)}) · 现货 ${product.stock_on_hand}</option>`).join("")
    : `<option value="">暂无可用商品</option>`;
  if (products.some((product) => String(product.id) === selected)) $("#order-product").value = selected;
}

async function loadCustomers() {
  try {
    const keyword = $("#customer-search").value.trim();
    const data = await api(`/api/customers?q=${encodeURIComponent(keyword)}`);
    state.customers = data.items || [];
    renderCustomers();
    renderCustomerOptions();
  } catch (error) { showError(error); }
}

function renderCustomers() {
  const table = $("#customer-table");
  $("#customer-count").textContent = `共 ${state.customers.length} 位客户`;
  if (!state.customers.length) {
    table.innerHTML = `<tr><td colspan="4" class="empty">还没有客户。先在左侧录入一位客户。</td></tr>`;
    return;
  }
  table.innerHTML = state.customers.map((customer) => `<tr><td><div class="item-title">${escapeHTML(customer.full_name)}</div><div class="item-subtitle">${escapeHTML(customer.note || "无备注")}</div></td><td>${escapeHTML(customer.phone)}<div class="item-subtitle">${escapeHTML(customer.email || "未填写邮箱")}</div></td><td>${escapeHTML(customer.identity_ref || "未填写")}</td><td>${dateTime(customer.created_at)}</td></tr>`).join("");
}

function renderCustomerOptions() {
  const selected = $("#order-customer").value;
  $("#order-customer").innerHTML = state.customers.length
    ? state.customers.map((customer) => `<option value="${customer.id}">${escapeHTML(customer.full_name)} · ${escapeHTML(customer.phone)}</option>`).join("")
    : `<option value="">请先创建客户</option>`;
  if (state.customers.some((customer) => String(customer.id) === selected)) $("#order-customer").value = selected;
}

async function loadOrders() {
  try {
    const params = new URLSearchParams({ limit: "100" });
    const type = $("#order-type-filter").value;
    const status = $("#order-status-filter").value;
    if (type) params.set("order_type", type);
    if (status) params.set("status", status);
    const data = await api(`/api/orders?${params.toString()}`);
    state.orders = data.items || [];
    renderOrders();
  } catch (error) { showError(error); }
}

function renderOrders() {
  const table = $("#order-table");
  if (!state.orders.length) {
    table.innerHTML = `<tr><td colspan="6" class="empty">没有符合条件的订单。</td></tr>`;
    return;
  }
  table.innerHTML = state.orders.map((order) => {
    const products = order.items.map((item) => `${escapeHTML(item.product_name)} × ${item.quantity}`).join("<br>");
    const period = order.order_type === "rental" ? `<div class="item-subtitle">${order.start_date} → ${order.end_date}（${order.rental_days} 天）</div>` : '<div class="item-subtitle">销售订单</div>';
    return `<tr><td><div class="item-title">${escapeHTML(order.order_no)}</div>${period}</td><td>${escapeHTML(order.customer_name)}<div class="item-subtitle">${escapeHTML(order.customer_phone)}</div></td><td>${products}</td><td><div class="item-title">${money(order.total_cents)}</div><div class="item-subtitle">已收 ${money(order.paid_cents)} · ${paymentLabel(order.payment_status)}</div></td><td><span class="status ${escapeHTML(order.status)}">${statusLabel(order.status)}</span></td><td><div class="actions">${orderActions(order)}</div></td></tr>`;
  }).join("");
}

function orderActions(order) {
  const parts = [`<button class="button secondary small" data-payment-order="${order.id}">收 / 退款</button>`];
  if (order.order_type === "rental" && order.status === "confirmed") {
    parts.push(`<button class="button small" data-status-order="${order.id}" data-target-status="checked_out">确认取货</button>`, `<button class="button danger small" data-status-order="${order.id}" data-target-status="cancelled">取消</button>`);
  } else if (order.order_type === "rental" && order.status === "checked_out") {
    parts.push(`<button class="button small" data-status-order="${order.id}" data-target-status="returned">确认归还</button>`);
  } else if (order.order_type === "sale" && order.status === "confirmed") {
    parts.push(`<button class="button small" data-status-order="${order.id}" data-target-status="completed">完成交付</button>`, `<button class="button danger small" data-status-order="${order.id}" data-target-status="cancelled">取消</button>`);
  }
  return parts.join("");
}

async function createProduct(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    await api("/api/products", { method: "POST", body: JSON.stringify({
      sku: form.get("sku"), name: form.get("name"), product_type: form.get("product_type"), category: form.get("category"), location: form.get("location"), description: form.get("description"),
      rent_enabled: form.get("rent_enabled") === "on", sell_enabled: form.get("sell_enabled") === "on",
      daily_rent_cents: cents(form.get("daily_rent")), sale_price_cents: cents(form.get("sale_price")), deposit_cents: cents(form.get("deposit")), stock_on_hand: integer(form.get("stock_on_hand")),
    }) });
    event.currentTarget.reset();
    event.currentTarget.querySelector('[name="product_type"]').value = "general";
    event.currentTarget.querySelector('[name="stock_on_hand"]').value = "1";
    event.currentTarget.querySelector('[name="rent_enabled"]').checked = true;
    event.currentTarget.querySelector('[name="sell_enabled"]').checked = true;
    showSuccess("商品已保存");
    await Promise.all([loadProducts(), loadDashboard()]);
  } catch (error) { showError(error); }
}

async function createCustomer(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    await api("/api/customers", { method: "POST", body: JSON.stringify(Object.fromEntries(form.entries())) });
    event.currentTarget.reset();
    showSuccess("客户已保存");
    await loadCustomers();
  } catch (error) { showError(error); }
}

function addOrderItem() {
  const productID = Number($("#order-product").value);
  const quantity = integer($("#order-quantity").value);
  const product = state.products.find((item) => item.id === productID);
  if (!product || quantity < 1) { showError(new Error("请选择商品，并填写大于 0 的数量")); return; }
  state.orderItems.set(productID, quantity + (state.orderItems.get(productID) || 0));
  renderOrderItems();
}

function renderOrderItems() {
  const container = $("#order-items");
  if (!state.orderItems.size) {
    container.innerHTML = `<div class="empty">尚未添加商品。</div>`;
    return;
  }
  container.innerHTML = [...state.orderItems.entries()].map(([productID, quantity]) => {
    const product = state.products.find((item) => item.id === productID);
    if (!product) return "";
    return `<div class="order-item"><div><div class="item-title">${escapeHTML(product.name)}</div><div class="item-subtitle">${escapeHTML(product.sku)} · 现货 ${product.stock_on_hand}</div></div><input type="number" min="1" value="${quantity}" data-order-item-quantity="${productID}" /><button type="button" class="button danger small" data-remove-order-item="${productID}">移除</button></div>`;
  }).join("");
}

async function createOrder(event) {
  event.preventDefault();
  await runButtonBusy(event.submitter, () => createOrderRequest(event.currentTarget));
}

async function createOrderRequest(formElement) {
  if (!state.orderItems.size) { showError(new Error("请至少添加一个商品")); return; }
  const form = new FormData(formElement);
  const body = {
    order_type: form.get("order_type"), customer_id: Number(form.get("customer_id")), items: [...state.orderItems.entries()].map(([product_id, quantity]) => ({ product_id, quantity })), notes: form.get("notes"),
  };
  if (body.order_type === "rental") { body.start_date = form.get("start_date"); body.end_date = form.get("end_date"); }
  try {
    const order = await api("/api/orders", { method: "POST", headers: { "Idempotency-Key": newIdempotencyKey() }, body: JSON.stringify(body) });
    state.orderItems.clear();
    renderOrderItems();
    formElement.querySelector('[name="notes"]').value = "";
    showSuccess(`订单 ${order.order_no} 已创建并锁定库存`);
    await Promise.all([loadProducts(), loadOrders(), loadDashboard()]);
    switchView("orders");
  } catch (error) { showError(error); }
}

function toggleRentalFields() {
  const rental = $("#order-type").value === "rental";
  document.querySelectorAll(".rental-only").forEach((element) => { element.style.display = rental ? "grid" : "none"; element.querySelector("input").required = rental; });
  state.orderItems.clear();
  renderOrderItems();
  renderOrderProductOptions();
}

async function handleDelegatedClick(event) {
  const remove = event.target.closest("[data-remove-order-item]");
  if (remove) { state.orderItems.delete(Number(remove.dataset.removeOrderItem)); renderOrderItems(); return; }
  const stock = event.target.closest("[data-stock-product]");
  if (stock) { await runButtonBusy(stock, () => adjustStock(Number(stock.dataset.stockProduct))); return; }
  const status = event.target.closest("[data-status-order]");
  if (status) { await runButtonBusy(status, () => changeStatus(Number(status.dataset.statusOrder), status.dataset.targetStatus)); return; }
  const payment = event.target.closest("[data-payment-order]");
  if (payment) { await runButtonBusy(payment, () => recordPayment(Number(payment.dataset.paymentOrder))); }
}

document.addEventListener("change", (event) => {
  const input = event.target.closest("[data-order-item-quantity]");
  if (!input) return;
  const quantity = integer(input.value);
  if (quantity > 0) state.orderItems.set(Number(input.dataset.orderItemQuantity), quantity);
  else state.orderItems.delete(Number(input.dataset.orderItemQuantity));
  renderOrderItems();
});

async function adjustStock(productID) {
  const delta = Number(window.prompt("输入库存调整数量（正数入库，负数出库）：", "0"));
  if (!Number.isInteger(delta) || delta === 0) return;
  const reason = window.prompt("填写调整原因：", "盘点调整");
  if (reason === null) return;
  try { await api(`/api/products/${productID}/stock-adjustments`, { method: "POST", headers: { "Idempotency-Key": newIdempotencyKey() }, body: JSON.stringify({ delta, reason }) }); showSuccess("库存已调整"); await Promise.all([loadProducts(), loadDashboard()]); } catch (error) { showError(error); }
}

async function changeStatus(orderID, status) {
  const confirmation = window.confirm(`确认将订单变更为“${statusLabel(status)}”？`);
  if (!confirmation) return;
  const note = window.prompt("操作备注（可选）：", "") ?? "";
  try { await api(`/api/orders/${orderID}/status`, { method: "POST", body: JSON.stringify({ status, note }) }); showSuccess("订单状态已更新"); await Promise.all([loadOrders(), loadProducts(), loadDashboard()]); } catch (error) { showError(error); }
}

async function recordPayment(orderID) {
  const direction = window.prompt("输入 charge 收款 或 refund 退款：", "charge");
  if (direction === null) return;
  const amount = window.prompt("输入金额（元）：", "");
  if (amount === null) return;
  const reference = window.prompt("支付流水号或业务参考号（必填）：", "") ?? "";
  if (!reference.trim()) { showError(new Error("请填写不可重复的支付流水号或业务参考号")); return; }
  try { await api(`/api/orders/${orderID}/payments`, { method: "POST", body: JSON.stringify({ direction, amount_cents: cents(amount), reference: reference.trim(), note: "后台手工登记" }) }); showSuccess("支付流水已登记"); await loadOrders(); } catch (error) { showError(error); }
}

function switchView(view) {
  const title = { dashboard: "概览", products: "商品与库存", customers: "客户", "new-order": "新建订单", orders: "订单中心" }[view] || "RentSale";
  $("#page-title").textContent = title;
  document.querySelectorAll(".view").forEach((element) => element.classList.toggle("active", element.dataset.view === view));
  document.querySelectorAll(".nav-link").forEach((element) => element.classList.toggle("active", element.dataset.viewLink === view));
  if (view === "new-order") renderOrderItems();
  window.scrollTo({ top: 0, behavior: "smooth" });
}

function setDefaultDates() {
  const today = new Date();
  const tomorrow = new Date(Date.now() + 86400000);
  $("#start-date").value = toDateInput(today);
  $("#end-date").value = toDateInput(tomorrow);
  $("#start-date").min = toDateInput(today);
  $("#end-date").min = toDateInput(tomorrow);
  renderOrderItems();
}

function money(centsValue) { return currency.format((Number(centsValue) || 0) / 100); }
function cents(value) { const amount = Number(value); if (!Number.isFinite(amount) || amount < 0) throw new Error("金额必须是大于或等于 0 的数字"); return Math.round(amount * 100); }
function integer(value) { const parsed = Number(value); return Number.isInteger(parsed) ? parsed : 0; }
function dateTime(value) { try { return dateFormatter.format(new Date(value)); } catch { return "—"; } }
function toDateInput(date) {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}
function newIdempotencyKey() {
  if (window.crypto?.randomUUID) return window.crypto.randomUUID();
  return `rs-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
async function runButtonBusy(button, action) {
  if (button?.disabled) return;
  if (button) button.disabled = true;
  try { await action(); } finally { if (button) button.disabled = false; }
}
function statusLabel(status) { return ({ confirmed: "已确认", checked_out: "租赁中", returned: "已归还", completed: "已完成", cancelled: "已取消" })[status] || status; }
function paymentLabel(status) { return ({ unpaid: "未收款", partial: "部分收款", paid: "已收清", refunded: "已退款" })[status] || status; }
function escapeHTML(value) { return String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#039;", '"': "&quot;" }[char])); }
function debounce(fn, wait) { let timeout; return (...args) => { window.clearTimeout(timeout); timeout = window.setTimeout(() => fn(...args), wait); }; }
function showSuccess(message) { showToast(message, "success"); }
function showError(error) { showToast(error.message || "操作失败", "error"); }
function showToast(message, type) { const toast = $("#toast"); toast.textContent = message; toast.className = `toast show ${type}`; window.clearTimeout(showToast.timer); showToast.timer = window.setTimeout(() => { toast.className = "toast"; }, 3600); }
