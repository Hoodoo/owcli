// owcli viewer: a scope picker, a force-directed page graph on a canvas, a
// reader with in-app link navigation, search, and per-page Claims. No build
// step and no dependencies; Mermaid is loaded from a CDN only when a page has
// a diagram, and diagrams fall back to their source without network.

const $ = (id) => document.getElementById(id);

async function api(path, params = {}) {
  const url = new URL(path, location.href);
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") url.searchParams.set(k, v);
  }
  const res = await fetch(url);
  const body = await res.json();
  if (!res.ok) throw new Error(body.error?.message || res.statusText);
  return body;
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children) node.append(c);
  return node;
}

// ---- state ----------------------------------------------------------------

const state = {
  scope: null, // {kind: "wiki" | "ws", id}
  nodes: [],
  edges: [],
  byId: new Map(),
  wikiNames: new Map(),
  selected: null,
  hover: null,
  page: null, // {wiki, page, id}: the page shown
  wanted: null, // node id of the page last requested
};

// Bumped by every navigation; async work for an older page is dropped.
let pageSeq = 0;

const PALETTE = ["#4e79a7", "#f28e2b", "#59a14f", "#b07aa1", "#76b7b2", "#edc948", "#9c755f", "#e15759", "#bab0ac", "#86bcb6"];
let colorKeys = new Map();

function scopeParams(scope = state.scope) {
  return scope.kind === "ws" ? { workspace: scope.id } : { wiki: scope.id };
}

// ---- URL hash: #scope=ws:<id>|wiki:<id>&page=<wiki>:<path>&a=<anchor> -----

function readHash() {
  const h = new URLSearchParams(location.hash.slice(1));
  const scope = h.get("scope");
  let parsed = null;
  if (scope) {
    const i = scope.indexOf(":");
    parsed = { kind: scope.slice(0, i), id: scope.slice(i + 1) };
  }
  return { scope: parsed, page: h.get("page"), anchor: h.get("a") };
}

function writeHash(push) {
  const h = new URLSearchParams();
  if (state.scope) h.set("scope", `${state.scope.kind}:${state.scope.id}`);
  if (state.page) h.set("page", state.page.id);
  const next = "#" + h.toString();
  if (next === location.hash) return;
  if (push) history.pushState(null, "", next);
  else history.replaceState(null, "", next);
}

// ---- scope picker -----------------------------------------------------------

async function buildPicker(config) {
  const listing = await api("/api/wikis");
  const select = $("scope");
  select.replaceChildren();
  const known = new Set();
  if (listing.workspaces.length) {
    const group = el("optgroup", { label: "Workspaces" });
    for (const ws of listing.workspaces) {
      group.append(el("option", { value: `ws:${ws.id}` }, `${ws.name} (${ws.wikis.length} wikis)`));
    }
    select.append(group);
  }
  const group = el("optgroup", { label: "Wikis" });
  for (const w of listing.wikis) {
    known.add(w.id);
    state.wikiNames.set(w.id, w.name);
    const opt = el("option", { value: `wiki:${w.id}` }, w.name + (w.problem ? " (unavailable)" : ""));
    if (w.problem) {
      opt.disabled = true;
      opt.title = w.problem;
    }
    group.append(opt);
  }
  // The repository serve started in may not be registered.
  for (const id of config.default.wikis || []) {
    if (!known.has(id)) group.append(el("option", { value: `wiki:${id}` }, id));
  }
  select.append(group);
  if (!select.options.length) {
    select.append(el("option", { value: "" }, "no wikis: bind a repository first"));
  }
  select.onchange = () => {
    const [kind, ...rest] = select.value.split(":");
    switchScope({ kind, id: rest.join(":") }, true);
  };
}

function defaultScope(config) {
  if (config.default.workspace) return { kind: "ws", id: config.default.workspace.id };
  if (config.default.wikis?.length) return { kind: "wiki", id: config.default.wikis[0] };
  const first = $("scope").querySelector("option:not([disabled])");
  if (!first || !first.value) return null;
  const [kind, ...rest] = first.value.split(":");
  return { kind, id: rest.join(":") };
}

async function switchScope(scope, push) {
  state.scope = scope;
  state.wanted = null;
  pageSeq++;
  $("scope").value = `${scope.kind}:${scope.id}`;
  showEmpty();
  await loadGraph();
  writeHash(push);
}

// ---- graph data and simulation ---------------------------------------------

async function loadGraph() {
  setStatus("loading…");
  let g;
  try {
    g = await api("/api/graph", scopeParams());
  } catch (e) {
    setStatus(e.message, true);
    state.nodes = [];
    state.edges = [];
    draw();
    return;
  }
  for (const w of g.wikis) state.wikiNames.set(w.id, w.name);
  const old = state.byId;
  state.byId = new Map();
  state.nodes = g.nodes.map((n, i) => {
    const prev = old.get(n.id);
    const angle = (i / Math.max(1, g.nodes.length)) * Math.PI * 2;
    const node = Object.assign(n, {
      x: prev ? prev.x : Math.cos(angle) * 200 + Math.random() * 20,
      y: prev ? prev.y : Math.sin(angle) * 200 + Math.random() * 20,
      vx: 0,
      vy: 0,
      r: 5 + Math.min(9, Math.sqrt(n.size) / 9),
    });
    state.byId.set(n.id, node);
    return node;
  });
  state.edges = g.edges.map((e) => ({ s: state.byId.get(e.source), t: state.byId.get(e.target) })).filter((e) => e.s && e.t);
  assignColors();
  const issues = state.nodes.filter((n) => n.stale + n.unresolved > 0).length;
  let status = `${state.nodes.length} pages, ${state.edges.length} links`;
  if (g.wikis.length > 1) status += `, ${g.wikis.length} wikis`;
  if (issues) status += `, ${issues} with Claims to recheck`;
  if (g.skipped.length) status += `; skipped: ${g.skipped.map((m) => m.wiki.id).join(", ")}`;
  setStatus(status, issues > 0);
  alpha = 1;
  for (let i = 0; i < 300 && alpha > 0.02; i++) tick();
  userMoved = false;
  fit();
  draw();
}

function setStatus(text, warn) {
  const s = $("status");
  s.textContent = text;
  s.style.color = warn ? "var(--warn)" : "";
}

function colorKey(n) {
  return $("colorBy").value === "wiki" ? state.wikiNames.get(n.wiki) || n.wiki : n.type;
}

function assignColors() {
  const keys = [...new Set(state.nodes.map(colorKey))].sort();
  colorKeys = new Map(keys.map((k, i) => [k, PALETTE[i % PALETTE.length]]));
  const legend = $("legend");
  legend.replaceChildren(...keys.map((k) => el("span", {}, el("i", { style: `background:${colorKeys.get(k)}` }), k)));
  legend.hidden = keys.length === 0;
}

let alpha = 0;
let animating = false;

function tick() {
  const nodes = state.nodes;
  alpha *= 0.985;
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i];
    for (let j = i + 1; j < nodes.length; j++) {
      const b = nodes[j];
      let dx = a.x - b.x;
      let dy = a.y - b.y;
      let d2 = dx * dx + dy * dy;
      if (d2 < 1) {
        dx = Math.random() - 0.5;
        dy = Math.random() - 0.5;
        d2 = 1;
      }
      const f = (4000 * alpha) / d2;
      const d = Math.sqrt(d2);
      a.vx += (dx / d) * f;
      a.vy += (dy / d) * f;
      b.vx -= (dx / d) * f;
      b.vy -= (dy / d) * f;
    }
  }
  for (const { s, t } of state.edges) {
    const dx = t.x - s.x;
    const dy = t.y - s.y;
    const d = Math.max(1, Math.sqrt(dx * dx + dy * dy));
    const f = (d - 110) * 0.03 * alpha;
    s.vx += (dx / d) * f;
    s.vy += (dy / d) * f;
    t.vx -= (dx / d) * f;
    t.vy -= (dy / d) * f;
  }
  for (const n of nodes) {
    n.vx -= n.x * 0.01 * alpha;
    n.vy -= n.y * 0.01 * alpha;
    if (n.fixed) {
      n.vx = n.vy = 0;
      continue;
    }
    n.vx *= 0.6;
    n.vy *= 0.6;
    n.x += n.vx;
    n.y += n.vy;
  }
}

function animate() {
  if (animating) return;
  animating = true;
  const step = () => {
    tick();
    draw();
    if (alpha > 0.01 || drag?.node) requestAnimationFrame(step);
    else animating = false;
  };
  requestAnimationFrame(step);
}

// ---- drawing ----------------------------------------------------------------

const canvas = $("graph");
const ctx = canvas.getContext("2d");
const view = { x: 0, y: 0, k: 1 };
let userMoved = false; // the reader panned or zoomed: keep their view on resize
let cssWidth = 0;
let cssHeight = 0;

function resize() {
  const rect = canvas.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  cssWidth = rect.width;
  cssHeight = rect.height;
  canvas.width = Math.round(rect.width * dpr);
  canvas.height = Math.round(rect.height * dpr);
  if (!userMoved) fit();
  draw();
}

function fit() {
  if (!state.nodes.length || !cssWidth) {
    Object.assign(view, { x: cssWidth / 2, y: cssHeight / 2, k: 1 });
    return;
  }
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const n of state.nodes) {
    minX = Math.min(minX, n.x - n.r);
    maxX = Math.max(maxX, n.x + n.r);
    minY = Math.min(minY, n.y - n.r);
    maxY = Math.max(maxY, n.y + n.r);
  }
  const pad = 60;
  const k = Math.min(2, (cssWidth - pad) / Math.max(1, maxX - minX), (cssHeight - pad) / Math.max(1, maxY - minY));
  view.k = k;
  view.x = cssWidth / 2 - ((minX + maxX) / 2) * k;
  view.y = cssHeight / 2 - ((minY + maxY) / 2) * k;
}

function css(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function neighbors(n) {
  return n ? new Set([n.id, ...n.links, ...n.backlinks]) : null;
}

function draw() {
  const dpr = window.devicePixelRatio || 1;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  ctx.setTransform(dpr * view.k, 0, 0, dpr * view.k, dpr * view.x, dpr * view.y);
  const focus = state.hover || state.selected;
  const near = neighbors(focus);
  const line = css("--line");
  const accent = css("--accent");
  const warn = css("--warn");
  const fg = css("--fg");
  const k = view.k;

  for (const { s, t } of state.edges) {
    const lit = focus && (s === focus || t === focus);
    ctx.globalAlpha = near && !lit ? 0.25 : 1;
    ctx.strokeStyle = lit ? accent : line;
    ctx.lineWidth = (lit ? 2 : 1) / k;
    const dx = t.x - s.x;
    const dy = t.y - s.y;
    const d = Math.max(1, Math.hypot(dx, dy));
    const ex = t.x - (dx / d) * (t.r + 2);
    const ey = t.y - (dy / d) * (t.r + 2);
    ctx.beginPath();
    ctx.moveTo(s.x, s.y);
    ctx.lineTo(ex, ey);
    ctx.stroke();
    const a = 7 / k;
    ctx.fillStyle = ctx.strokeStyle;
    ctx.beginPath();
    ctx.moveTo(ex, ey);
    ctx.lineTo(ex - (dx / d) * a - (dy / d) * a * 0.5, ey - (dy / d) * a + (dx / d) * a * 0.5);
    ctx.lineTo(ex - (dx / d) * a + (dy / d) * a * 0.5, ey - (dy / d) * a - (dx / d) * a * 0.5);
    ctx.closePath();
    ctx.fill();
  }

  for (const n of state.nodes) {
    ctx.globalAlpha = near && !near.has(n.id) ? 0.3 : 1;
    ctx.fillStyle = colorKeys.get(colorKey(n)) || PALETTE[0];
    ctx.beginPath();
    ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2);
    ctx.fill();
    if (n.stale + n.unresolved > 0) {
      ctx.strokeStyle = warn;
      ctx.lineWidth = 3 / k;
      ctx.stroke();
    }
    if (n === state.selected) {
      ctx.strokeStyle = accent;
      ctx.lineWidth = 3 / k;
      ctx.beginPath();
      ctx.arc(n.x, n.y, n.r + 4 / k, 0, Math.PI * 2);
      ctx.stroke();
    }
  }

  ctx.font = `${12 / k}px system-ui, sans-serif`;
  ctx.textAlign = "center";
  ctx.textBaseline = "top";
  for (const n of state.nodes) {
    const show = k > 0.9 || (near && near.has(n.id)) || state.nodes.length <= 25;
    if (!show) continue;
    ctx.globalAlpha = near && !near.has(n.id) ? 0.3 : 1;
    ctx.fillStyle = fg;
    ctx.fillText(n.title, n.x, n.y + n.r + 3 / k);
  }
  ctx.globalAlpha = 1;
}

// ---- interaction --------------------------------------------------------------

function toWorld(e) {
  const rect = canvas.getBoundingClientRect();
  return { x: (e.clientX - rect.left - view.x) / view.k, y: (e.clientY - rect.top - view.y) / view.k };
}

function hit(p) {
  for (let i = state.nodes.length - 1; i >= 0; i--) {
    const n = state.nodes[i];
    if (Math.hypot(n.x - p.x, n.y - p.y) <= n.r + 4 / view.k) return n;
  }
  return null;
}

let drag = null;

canvas.addEventListener("pointerdown", (e) => {
  canvas.setPointerCapture(e.pointerId);
  const p = toWorld(e);
  const node = hit(p);
  drag = { node, startX: e.clientX, startY: e.clientY, viewX: view.x, viewY: view.y, moved: false };
  if (node) node.fixed = true;
  canvas.classList.add("dragging");
});

canvas.addEventListener("pointermove", (e) => {
  if (drag) {
    if (Math.hypot(e.clientX - drag.startX, e.clientY - drag.startY) > 3) drag.moved = true;
    if (drag.node) {
      const p = toWorld(e);
      drag.node.x = p.x;
      drag.node.y = p.y;
      if (drag.moved) {
        alpha = Math.max(alpha, 0.3);
        animate();
      }
    } else {
      view.x = drag.viewX + e.clientX - drag.startX;
      view.y = drag.viewY + e.clientY - drag.startY;
      userMoved = true;
      draw();
    }
    return;
  }
  const node = hit(toWorld(e));
  if (node !== state.hover) {
    state.hover = node;
    draw();
  }
  const tip = $("tooltip");
  if (node) {
    const rect = canvas.getBoundingClientRect();
    tip.replaceChildren(el("strong", {}, node.title), el("br"), node.description || node.page);
    if (node.stale + node.unresolved) tip.append(el("br"), el("span", { style: "color:var(--warn)" }, `${node.stale} stale, ${node.unresolved} unresolved Claim(s)`));
    tip.style.left = `${e.clientX - rect.left + 14}px`;
    tip.style.top = `${e.clientY - rect.top + 14}px`;
    tip.hidden = false;
  } else {
    tip.hidden = true;
  }
});

canvas.addEventListener("pointerup", () => {
  if (drag?.node) {
    drag.node.fixed = false;
    if (!drag.moved) openPage(drag.node.id, null, true);
  }
  drag = null;
  canvas.classList.remove("dragging");
});

canvas.addEventListener("pointerleave", () => {
  $("tooltip").hidden = true;
  if (state.hover) {
    state.hover = null;
    draw();
  }
});

canvas.addEventListener(
  "wheel",
  (e) => {
    e.preventDefault();
    const rect = canvas.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    const k = Math.min(6, Math.max(0.1, view.k * Math.exp(-e.deltaY * 0.0015)));
    view.x = mx - ((mx - view.x) * k) / view.k;
    view.y = my - ((my - view.y) * k) / view.k;
    view.k = k;
    userMoved = true;
    draw();
  },
  { passive: false },
);

$("colorBy").addEventListener("change", () => {
  assignColors();
  draw();
});

new ResizeObserver(resize).observe(canvas);

// ---- reader -------------------------------------------------------------------

function showEmpty() {
  state.page = null;
  $("page").hidden = true;
  $("empty").hidden = false;
  state.selected = null;
  draw();
}

function splitNodeId(id) {
  const i = id.indexOf(":");
  return { wiki: id.slice(0, i), page: id.slice(i + 1) + ".md" };
}

// resolve joins a relative link to the directory of a wiki-relative page.
function resolve(fromPage, href) {
  const parts = fromPage.split("/").slice(0, -1);
  for (const seg of href.split("/")) {
    if (seg === "..") parts.pop();
    else if (seg && seg !== ".") parts.push(seg);
  }
  return parts.join("/");
}

async function openPage(nodeId, anchor, push) {
  const { wiki, page } = splitNodeId(nodeId);
  const seq = ++pageSeq;
  state.wanted = nodeId;
  let p;
  try {
    p = await api("/api/page", { wiki, page });
  } catch (e) {
    setStatus(e.message, true);
    return;
  }
  if (seq !== pageSeq) return; // a later navigation won
  state.page = { wiki, page, id: nodeId };
  state.selected = state.byId.get(nodeId) || null;
  draw();
  writeHash(push);

  $("empty").hidden = true;
  $("page").hidden = false;
  $("crumbs").textContent = `${state.wikiNames.get(wiki) || wiki} · ${page}`;
  $("title").textContent = p.title;
  $("description").textContent = p.description;
  const badges = [el("span", { class: "badge" }, p.type), ...p.tags.map((t) => el("span", { class: "badge" }, "#" + t))];
  const issues = p.claims.filter((c) => c.issue).length;
  if (p.claims.length) {
    badges.push(issues ? el("span", { class: "badge warn" }, `${issues} of ${p.claims.length} Claims to recheck`) : el("span", { class: "badge ok" }, `${p.claims.length} Claims current`));
  } else {
    badges.push(el("span", { class: "badge" }, "no Claims"));
  }
  $("badges").replaceChildren(...badges);

  const content = $("content");
  content.innerHTML = p.html; // server-rendered; raw HTML in pages is dropped
  // The page's own H1 repeats the title shown above it.
  const h1 = content.querySelector("h1");
  if (h1 && content.firstElementChild === h1 && h1.textContent.trim() === p.title.trim()) h1.remove();
  wireLinks(content, wiki, page);
  renderMermaid(content, seq);

  $("claimsSummary").textContent = `Claims (${p.claims.length})` + (issues ? `, ${issues} to recheck` : "");
  $("claims").replaceChildren(
    ...p.claims.map((c) =>
      el(
        "li",
        {},
        c.statement,
        el("div", { class: "evidence" }, c.evidence.join("  ")),
        ...(c.issue ? [el("div", { class: "issue" }, `${c.issue.kind}: ${c.issue.resources.join(", ")}`)] : []),
      ),
    ),
  );
  $("claimsBox").open = issues > 0;
  const linkList = (ids) =>
    ids.length
      ? ids.map((id) => el("li", {}, el("a", { href: "#", onclick: (e) => (e.preventDefault(), openPage(id, null, true)) }, state.byId.get(id)?.title || splitNodeId(id).page)))
      : [el("li", { class: "muted" }, "none")];
  $("links").replaceChildren(...linkList(p.links));
  $("backlinks").replaceChildren(...linkList(p.backlinks));

  if (anchor) scrollToAnchor(anchor);
  else $("reader").scrollTop = 0;
}

function scrollToAnchor(anchor) {
  const target = document.getElementById(anchor);
  if (!target) return;
  target.scrollIntoView({ block: "start" });
  target.classList.add("flash");
  setTimeout(() => target.classList.remove("flash"), 1500);
}

function wireLinks(root, wiki, page) {
  for (const a of root.querySelectorAll("a[href]")) {
    const href = a.getAttribute("href");
    if (/^[a-z][a-z\d+.-]*:|^\/\//i.test(href)) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      continue;
    }
    const [target, anchor] = href.split("#");
    if (!target) {
      a.addEventListener("click", (e) => (e.preventDefault(), scrollToAnchor(decodeURIComponent(anchor || ""))));
      continue;
    }
    const path = resolve(page, decodeURIComponent(target));
    if (path.toLowerCase().endsWith(".md")) {
      const id = `${wiki}:${path.slice(0, -3)}`;
      a.addEventListener("click", (e) => (e.preventDefault(), openPage(id, anchor ? decodeURIComponent(anchor) : null, true)));
    } else {
      // A link into the repository's source: the viewer cannot open it.
      a.removeAttribute("href");
      a.title = `source: ${path}`;
      a.classList.add("source");
    }
  }
}

let mermaidLoader = null;

function loadMermaid() {
  mermaidLoader ??= import("https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs").then((m) => {
    const dark = matchMedia("(prefers-color-scheme: dark)").matches;
    m.default.initialize({ startOnLoad: false, securityLevel: "strict", theme: dark ? "dark" : "default" });
    return m.default;
  });
  return mermaidLoader;
}

// Diagrams render one at a time, and only while their page is still shown:
// Mermaid measures the live DOM and fails on nodes that were replaced.
let mermaidQueue = Promise.resolve();

async function renderMermaid(root, seq) {
  const blocks = [...root.querySelectorAll("pre > code.language-mermaid")];
  if (!blocks.length) return;
  const divs = blocks.map((code) => {
    const div = el("div", { class: "mermaid" }, code.textContent);
    code.parentElement.replaceWith(div);
    return div;
  });
  try {
    const mermaid = await loadMermaid();
    // A run interrupted by navigation fails; it must not block later runs.
    const run = mermaidQueue.catch(() => {}).then(async () => {
      if (seq !== pageSeq) return;
      await mermaid.run({ nodes: divs.filter((d) => d.isConnected) });
    });
    mermaidQueue = run;
    await run;
  } catch {
    for (const d of divs) {
      d.style.whiteSpace = "pre";
      d.title = "Diagram source: Mermaid could not be loaded (offline?)";
    }
  }
}

// ---- search -------------------------------------------------------------------

let searchTimer = 0;
let searchSeq = 0;

$("q").addEventListener("input", () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(runSearch, 200);
});

$("q").addEventListener("keydown", (e) => {
  const items = [...$("results").children];
  const i = items.findIndex((li) => li.classList.contains("active"));
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    const next = e.key === "ArrowDown" ? Math.min(items.length - 1, i + 1) : Math.max(0, i - 1);
    items.forEach((li, j) => li.classList.toggle("active", j === next));
  } else if (e.key === "Enter") {
    (items[i] || items[0])?.click();
  } else if (e.key === "Escape") {
    $("results").hidden = true;
  }
});

document.addEventListener("click", (e) => {
  if (!e.target.closest(".search")) $("results").hidden = true;
});

async function runSearch() {
  const q = $("q").value.trim();
  const list = $("results");
  if (q.length < 2 || !state.scope) {
    list.hidden = true;
    return;
  }
  const seq = ++searchSeq;
  let r;
  try {
    r = await api("/api/search", { q, limit: 10, ...scopeParams() });
  } catch (e) {
    list.replaceChildren(el("li", { class: "muted" }, e.message));
    list.hidden = false;
    return;
  }
  if (seq !== searchSeq) return;
  if (!r.results.length) {
    list.replaceChildren(el("li", { class: "muted" }, "no results"));
  } else {
    list.replaceChildren(
      ...r.results.map((res) => {
        const lines = res.content.split("\n");
        const [path, anchor] = res.ref[0].replace(/^openwiki\//, "").split("#");
        const id = `${res.wiki}:${path.replace(/\.md$/, "")}`;
        return el(
          "li",
          {
            onclick: () => {
              list.hidden = true;
              openPage(id, anchor, true);
            },
          },
          el("div", {}, lines[0] + (lines[1]?.startsWith("Section: ") ? " · " + lines[1].slice(9) : "")),
          el("div", { class: "ref" }, (r.wikis.length > 1 ? `${state.wikiNames.get(res.wiki) || res.wiki} · ` : "") + res.ref[0]),
          el("div", { class: "snippet" }, lines[lines.length - 1].slice(0, 160)),
        );
      }),
    );
  }
  list.hidden = false;
}

// ---- start --------------------------------------------------------------------

async function applyHash() {
  const h = readHash();
  const scope = h.scope;
  if (scope && (!state.scope || scope.kind !== state.scope.kind || scope.id !== state.scope.id)) {
    state.scope = scope;
    $("scope").value = `${scope.kind}:${scope.id}`;
    await loadGraph();
  }
  // Compare with the page last asked for, not the one shown: during quick
  // navigation the shown page lags behind.
  if (h.page && h.page !== state.wanted) await openPage(h.page, h.anchor, false);
  else if (!h.page && state.wanted) {
    state.wanted = null;
    pageSeq++;
    showEmpty();
  }
}

window.addEventListener("popstate", applyHash);

(async () => {
  let config;
  try {
    config = await api("/api/config");
    await buildPicker(config);
  } catch (e) {
    setStatus(e.message, true);
    return;
  }
  resize();
  const h = readHash();
  const scope = h.scope || defaultScope(config);
  if (!scope) {
    setStatus("No wikis yet: run `owcli bind` in a repository, or `owcli init`.", true);
    return;
  }
  state.scope = scope;
  $("scope").value = `${scope.kind}:${scope.id}`;
  await loadGraph();
  if (h.page) await openPage(h.page, h.anchor, false);
  writeHash(false);
})();

// Read-only handle for debugging and browser tests.
window.__owcli = { state, view };
