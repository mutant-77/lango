/* ═══════════════════════════════════════════════════════════════
   Lango — frontend (Phases 0-7)
   ═══════════════════════════════════════════════════════════════ */

// El puerto lo resuelve el backend en tiempo de arranque (puede cambiar si
// Windows reserva el preferido), así que lo derivamos del origen que sirve esta
// página en vez de hardcodearlo.
const BASE = location.origin;
const WS = window.LANGO_WS_BASE || `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}`;
const PROMPTS_ID = 'prompts';

// terminal
let _term = null;
let _fit = null;
let _termWs = null;
let _activeId = null;
let _termInstId = null;
let _termId = '1';
const _activeTerm = new Map();
const _termScroll = new Map();
// xterm.js 6.1 respeta DEC 2026 (synchronized output) y difiere el sync del
// viewport durante un bloque 2026, así que ED2 dentro del frame ya no yánkea
// el scroll. No hace falta batching por rAF ni suppress de fit().

// file tree
let _filesWs = null;
let _treeInstId = null;
let _expanded = new Map();  // relPath → bool (only dirs)

// git state
let _git = { instId: null, ready: false, repo: false, branch: '', detached: false, truncated: false, total: 0, files: new Map(), dirs: new Set() };
let _gitGen = 0;
let _gitCtl = null;
const _gitCounts = new Map(); // instId → {total, truncated}
const _countQueue = new Set();
let _countBusy = false;
let _countCurrent = null;
let _gitPending = false;
let _gitDebounceTimer = null;
let _gitMaxTimer = null;
let _gitFirstScheduled = 0;
const _changesFilterIds = new Set();
const changesFilterOn = () => _changesFilterIds.has(_activeId);
let _change = null;
let _diffHovered = false;
let _diffDebounceTimer = null;
let _hlStyle = null;
let _highlightCode = null;

// editor
let _editorView = null;
let _langComp = null;
let _wordWrap = localStorage.getItem('lango_word_wrap') === 'true';
let _wrapComp = null;
let _EditorView = null;
let _openInstId = null;
let _openPath = null;
let _openMtime = null;
let _dirty = false;
let _diskChanged = false;
let _progSet = false;     // suppress dirty flag during programmatic doc change
const _langCache = {};

// Full folder name from the path (server's inst.name is truncated to 10 chars)
function projectName(inst) {
  const parts = (inst.path || '').split(/[\\/]+/).filter(Boolean);
  return parts[parts.length - 1] || inst.name;
}

// ── Phase 1: window controls ──────────────────────────────────────────
function initWindowControls() {
  document.getElementById('win-min')?.addEventListener('click', () =>
    window.pywebview?.api?.minimize());
  document.getElementById('win-max')?.addEventListener('click', () =>
    window.pywebview?.api?.toggle_maximize());
  document.getElementById('win-close')?.addEventListener('click', () =>
    window.pywebview?.api?.close());
}

// ── Phase 2: instances ────────────────────────────────────────────────
const _sessions = new Map();
let _sessionOrder = [];
let _listHover = false;
let _layoutPending = false;
let _listGen = 0;
let _listBusy = 0;
let _pollInFlight = false;
let _sepEl = null;

function getSepElement() {
  if (!_sepEl) {
    _sepEl = document.createElement('div');
    _sepEl.className = 'act-sep';
  }
  return _sepEl;
}

function groupOf(state) {
  if (state === 'busy' || state === 'waiting') return 0;
  if (state === 'used') return 1;
  return 2;
}

function computeLayout(sessions, sessionOrder) {
  const getInst = id => (typeof sessions?.get === 'function' ? sessions.get(id) : sessions?.[id]);
  const validIds = (sessionOrder || []).filter(id => Boolean(getInst(id)));

  const indexMap = new Map();
  validIds.forEach((id, idx) => indexMap.set(id, idx));

  const order = [...validIds].sort((a, b) => {
    const instA = getInst(a);
    const instB = getInst(b);
    const gA = groupOf(instA ? instA.state : 'fresh');
    const gB = groupOf(instB ? instB.state : 'fresh');
    if (gA !== gB) return gA - gB;
    return (indexMap.get(a) ?? 0) - (indexMap.get(b) ?? 0);
  });

  const compact = new Set();
  let freshCount = 0;
  let otherCount = 0;

  for (const id of validIds) {
    const inst = getInst(id);
    const state = inst ? inst.state : 'fresh';
    if (state === 'fresh') {
      compact.add(id);
      freshCount++;
    } else {
      otherCount++;
    }
  }

  const needSep = freshCount > 0 && otherCount > 0;

  return {
    order,
    compact,
    needSep,
    freshCount,
  };
}

function relocateNodes(bar, desiredNodes) {
  function getLISIndices(items) {
    const n = items.length;
    if (n === 0) return new Set();
    const dp = new Array(n).fill(1);
    const prev = new Array(n).fill(-1);
    let maxLen = 1;
    let bestEnd = 0;
    for (let i = 0; i < n; i++) {
      for (let j = 0; j < i; j++) {
        if (items[j].val < items[i].val && dp[j] + 1 > dp[i]) {
          dp[i] = dp[j] + 1;
          prev[i] = j;
        }
      }
      if (dp[i] > maxLen) {
        maxLen = dp[i];
        bestEnd = i;
      }
    }
    const lis = new Set();
    let curr = bestEnd;
    while (curr !== -1) {
      lis.add(items[curr].index);
      curr = prev[curr];
    }
    return lis;
  }

  const barChildren = Array.from(bar.children);
  const valid = [];
  for (let i = 0; i < desiredNodes.length; i++) {
    const idx = barChildren.indexOf(desiredNodes[i]);
    if (idx !== -1) {
      valid.push({ index: i, val: idx });
    }
  }

  const lis = getLISIndices(valid);
  let moves = 0;
  let ref = null;

  for (let i = desiredNodes.length - 1; i >= 0; i--) {
    const node = desiredNodes[i];
    if (lis.has(i)) {
      ref = node;
    } else {
      bar.insertBefore(node, ref);
      moves++;
      ref = node;
    }
  }

  const desiredSet = new Set(desiredNodes);
  for (const child of Array.from(bar.children)) {
    if (!desiredSet.has(child)) {
      if (typeof child.remove === 'function') {
        child.remove();
      } else if (typeof bar.remove === 'function') {
        bar.remove(child);
      }
    }
  }

  return moves;
}

function clearFlipStyles(card) {
  if (card._flipCleanup) {
    card._flipCleanup();
  } else {
    card.style.transform = '';
    card.style.transition = '';
  }
}

function markActive(id) {
  document.querySelectorAll('#activitybar .act').forEach(el => {
    el.classList.toggle('active', el.dataset.id === id);
  });
}

function updateListFade(target) {
  const el = target && target.classList ? target : (target && target.target && target.target.classList ? target.target : null);
  const bar = el || (typeof document !== 'undefined' ? document.getElementById('activitybar') : null);
  if (!bar) return;
  const { scrollTop, clientHeight, scrollHeight } = bar;
  bar.classList.toggle('fade-top', scrollTop > 0);
  bar.classList.toggle('fade-bottom', scrollTop + clientHeight < scrollHeight - 1);
}

function applyLayout() {
  const bar = document.getElementById('activitybar');
  if (!bar) {
    _layoutPending = false;
    updateListFade();
    return;
  }

  const layout = computeLayout(_sessions, _sessionOrder);
  const currentActCards = Array.from(bar.querySelectorAll('.act'));

  // 1. Early exit (Requirement 4):
  // Check if current DOM cards order, compact classes, and separator match desired layout
  const expectedChildCount = layout.order.length + (layout.needSep ? 1 : 0);
  let matches = bar.children.length === expectedChildCount && currentActCards.length === layout.order.length;

  if (matches) {
    for (let i = 0; i < layout.order.length; i++) {
      const card = currentActCards[i];
      const id = layout.order[i];
      if (card.dataset.id !== id || card.classList.contains('compact') !== layout.compact.has(id)) {
        matches = false;
        break;
      }
    }
  }

  if (matches) {
    const currentSep = bar.querySelector('.act-sep');
    if (Boolean(currentSep) !== layout.needSep) {
      matches = false;
    } else if (layout.needSep) {
      const firstCompactId = layout.order.find(id => layout.compact.has(id));
      const firstCompactCard = firstCompactId ? bar.querySelector(`.act[data-id="${firstCompactId}"]`) : null;
      if (!firstCompactCard || currentSep.nextElementSibling !== firstCompactCard) {
        matches = false;
      }
    }
  }

  if (matches) {
    if (layout.needSep) {
      const sep = bar.querySelector('.act-sep') || getSepElement();
      sep.textContent = `Not started (${layout.freshCount})`;
    }
    _layoutPending = false;
    updateListFade();
    return;
  }

  // 2. Prepare FLIP: measure current positions before changes
  for (const card of currentActCards) {
    clearFlipStyles(card);
  }

  const oldTops = new Map();
  for (const card of currentActCards) {
    oldTops.set(card.dataset.id, card.getBoundingClientRect().top);
  }

  let activeWasVisible = false;
  const activeCard = _activeId ? bar.querySelector(`.act[data-id="${_activeId}"]`) : null;
  if (activeCard) {
    const bRect = bar.getBoundingClientRect();
    const cRect = activeCard.getBoundingClientRect();
    activeWasVisible = (cRect.top < bRect.bottom && cRect.bottom > bRect.top);
  }

  // 3. Apply compact classes
  for (const card of currentActCards) {
    card.classList.toggle('compact', layout.compact.has(card.dataset.id));
  }

  // 4. Handle separator
  let sepNode = null;
  if (layout.needSep) {
    sepNode = getSepElement();
    sepNode.textContent = `Not started (${layout.freshCount})`;
  } else if (_sepEl && _sepEl.parentNode === bar) {
    _sepEl.remove();
  }

  // 5. Build desired sequence of nodes
  const cardMap = new Map();
  for (const card of currentActCards) {
    cardMap.set(card.dataset.id, card);
  }

  const desiredNodes = [];
  let sepPlaced = false;
  for (const id of layout.order) {
    if (layout.needSep && !sepPlaced && layout.compact.has(id)) {
      desiredNodes.push(sepNode);
      sepPlaced = true;
    }
    const card = cardMap.get(id);
    if (card) desiredNodes.push(card);
  }

  // 6. Relocate DOM nodes
  relocateNodes(bar, desiredNodes);

  // 7. Measure new tops and animate (FLIP)
  const prefersReduced = Boolean(
    typeof window !== 'undefined' &&
    window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
  );

  const animatingCards = [];
  let activeMoved = false;

  for (const card of currentActCards) {
    const id = card.dataset.id;
    if (!oldTops.has(id)) continue;
    const oldTop = oldTops.get(id);
    const newTop = card.getBoundingClientRect().top;
    const delta = oldTop - newTop;
    if (Math.abs(delta) > 0.5) {
      if (id === _activeId) {
        activeMoved = true;
      }
      if (!prefersReduced) {
        animatingCards.push({ card, delta });
      }
    }
  }

  if (animatingCards.length > 0) {
    for (const { card, delta } of animatingCards) {
      card.style.transition = 'none';
      card.style.transform = `translateY(${delta}px)`;
    }

    const rAF = (typeof requestAnimationFrame === 'function')
      ? requestAnimationFrame
      : (cb => setTimeout(cb, 16));

    rAF(() => {
      for (const { card } of animatingCards) {
        card.style.transition = 'transform 150ms ease';
        card.style.transform = 'none';

        const cleanup = () => {
          card.removeEventListener('transitionend', onEnd);
          clearTimeout(fallbackTimer);
          card._flipCleanup = null;
          card.style.transform = '';
          card.style.transition = '';
        };

        const onEnd = (e) => {
          if (e.target === card && e.propertyName === 'transform') {
            cleanup();
          }
        };

        const fallbackTimer = setTimeout(cleanup, 250);
        card._flipCleanup = cleanup;
        card.addEventListener('transitionend', onEnd);
      }
    });
  }

  // 8. Scroll active card into view if it was visible before and moved
  if (activeMoved && activeWasVisible && activeCard) {
    activeCard.scrollIntoView({ block: 'nearest' });
  }

  _layoutPending = false;
  updateListFade();
}

function parentFolder(path) {
  const parts = (path || '').split(/[\\/]+/).filter(Boolean);
  return parts.length >= 2 ? parts[parts.length - 2] : '';
}

function fmtAge(s) {
  if (s == null || typeof s !== 'number' || isNaN(s) || s < 60) return 'now';
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

function statusText(inst) {
  if (!inst) return '';
  switch (inst.state) {
    case 'busy':
      return 'working…';
    case 'waiting':
      return `waiting · ${fmtAge(inst.idle_s)}`;
    case 'used':
      return `idle · ${fmtAge(inst.idle_s)}`;
    case 'fresh':
    default:
      return '';
  }
}

function computeDisambiguation(sessions, sessionOrder) {
  const byName = new Map();
  for (const id of sessionOrder) {
    const inst = sessions.get(id);
    if (!inst) continue;
    const name = projectName(inst) || '';
    const key = name.toLowerCase();
    if (!byName.has(key)) byName.set(key, []);
    byName.get(key).push(inst);
  }

  const results = new Map();
  for (const [key, group] of byName) {
    if (group.length > 1) {
      const pathCounts = new Map();
      for (const inst of group) {
        const norm = (inst.path || '').toLowerCase();
        const count = (pathCounts.get(norm) || 0) + 1;
        pathCounts.set(norm, count);

        const parent = parentFolder(inst.path);
        const suffix = count > 1 ? ` #${count}` : '';
        const text = parent ? `${parent}${suffix}` : (suffix.trim());
        results.set(inst.id, { text, hidden: false });
      }
    } else {
      const inst = group[0];
      results.set(inst.id, { text: '', hidden: true });
    }
  }
  return results;
}

function updateCard(el, inst) {
  el.dataset.state = inst.state;
  const st = statusText(inst);
  el.title = `${inst.path}\n${st || 'Not started'}`;
  const proj = el.querySelector('.act-project');
  if (proj) proj.textContent = projectName(inst);
  const sub = el.querySelector('.act-sub');
  if (sub) {
    sub.textContent = st;
    const c = _gitCounts.get(inst.id);
    if (c && c.total > 0) {
      const span = document.createElement('span');
      span.className = 'act-changes';
      span.textContent = c.truncated ? '· 500+ changes' : `· ${c.total} ${c.total === 1 ? 'change' : 'changes'}`;
      sub.appendChild(span);
    }
  }
  const dot = el.querySelector('.act-status');
  if (dot) {
    dot.dataset.state = inst.state;
    dot.dataset.agent = inst.agent || 'none';
  }
  const tag = el.querySelector('.act-tag');
  if (tag) {
    tag.hidden = !inst.agent;
    tag.textContent = inst.agent || '';
  }
}

function makeActItem(inst, active) {
  const div = document.createElement('div');
  div.className = 'act' + (active ? ' active' : '');
  div.dataset.id = inst.id;
  div.innerHTML = `<span class="act-status agent-dot"></span><div class="act-body">` +
      `<div class="act-line"><span class="act-project"></span><span class="act-hint" hidden></span>` +
        `<span class="act-tag" hidden></span><div class="act-close" title="Close session">✕</div></div>` +
      `<div class="act-sub"></div>` +
    `</div>`;
  updateCard(div, inst);
  div.addEventListener('click', e => {
    if (e.target.closest('.act-close')) {
      e.stopPropagation();
      closeSession(inst.id);
      return;
    }
    activateInstance(inst.id);
  });
  return div;
}

function renderSessions() {
  const bar = document.getElementById('activitybar');
  if (!bar) return;

  const currentCards = bar.querySelectorAll('.act');
  const domMap = new Map();
  for (const card of currentCards) {
    const id = card.dataset.id;
    if (id) domMap.set(id, card);
  }

  // 1. Elimina del DOM las tarjetas cuyo id ya no existe.
  for (const [id, card] of domMap) {
    if (!_sessions.has(id)) {
      card.remove();
      domMap.delete(id);
    }
  }

  // 2. Crea con makeActItem las que faltan, insertándolas al final.
  for (const id of _sessionOrder) {
    if (!domMap.has(id)) {
      const inst = _sessions.get(id);
      if (inst) {
        const card = makeActItem(inst, inst.id === _activeId);
        bar.appendChild(card);
        domMap.set(id, card);
      }
    }
  }

  // 3. Llama a updateCard(el, inst) en todas (solo contenido: punto, etiqueta, texto, title).
  for (const id of _sessionOrder) {
    const card = domMap.get(id);
    const inst = _sessions.get(id);
    if (card && inst) {
      updateCard(card, inst);
    }
  }

  // 4. Desambiguación (D13)
  const hints = computeDisambiguation(_sessions, _sessionOrder);
  for (const [id, hintInfo] of hints) {
    const card = domMap.get(id);
    if (!card) continue;
    const hintEl = card.querySelector('.act-hint');
    if (hintEl) {
      hintEl.textContent = hintInfo.text;
      hintEl.hidden = hintInfo.hidden;
    }
  }

  // 5. Si _listHover es verdadero, marca _layoutPending = true; si no, llama a applyLayout().
  if (_listHover) {
    _layoutPending = true;
  } else {
    applyLayout();
  }
}

function applyInstances(list) {
  const prevStates = new Map();
  for (const [id, inst] of _sessions) prevStates.set(id, inst.state);
  _sessionOrder = (list || []).map(i => i.id);
  const currentIds = new Set(_sessionOrder);
  for (const id of _sessions.keys()) {
    if (!currentIds.has(id)) _sessions.delete(id);
  }
  for (const id of _gitCounts.keys()) {
    if (!currentIds.has(id)) _gitCounts.delete(id);
  }
  for (const inst of (list || [])) {
    _sessions.set(inst.id, inst);
  }
  renderSessions();
  if (_activeId) {
    renderTermTabs(_activeId);
  }
  for (const [id, inst] of _sessions) {
    if (prevStates.get(id) === 'busy' && inst.state !== 'busy') {
      if (id === _activeId) scheduleGitRefresh();
      else enqueueCount(id);
    } else if (id !== _activeId && id !== _countCurrent && !_gitCounts.has(id)) {
      enqueueCount(id);
    }
  }
}

function enqueueCount(id) {
  if (id === _activeId) return;
  _countQueue.add(id);
  drainCounts();
}

async function drainCounts() {
  if (_countBusy) return;
  _countBusy = true;
  try {
    while (_countQueue.size) {
      const id = _countQueue.values().next().value;
      _countQueue.delete(id);
      if (id === _activeId || !_sessions.has(id)) continue;
      _countCurrent = id;
      const before = _gitCounts.get(id);
      console.debug('git count', id);
      let data;
      try {
        const res = await fetch(`${BASE}/git/status?id=${encodeURIComponent(id)}`);
        data = res.ok ? await res.json() : { repo: false };
      } catch {
        data = { repo: false };
      }
      _countCurrent = null;
      if (id === _activeId || !_sessions.has(id) || _gitCounts.get(id) !== before) continue;
      const repo = Boolean(data && data.repo);
      _gitCounts.set(id, {
        total: repo && typeof data.total === 'number' ? data.total : 0,
        truncated: repo && Boolean(data.truncated),
      });
      renderSessions();
    }
  } finally {
    _countBusy = false;
    _countCurrent = null;
  }
}

async function pollInstances() {
  if (_pollInFlight) return null;
  _pollInFlight = true;
  const gen = _listGen;
  try {
    const r = await fetch(`${BASE}/instances`);
    if (!r.ok) return null;
    const data = await r.json();
    if (_listBusy > 0 || gen !== _listGen) return null;
    applyInstances(data.instances);
    return data;
  } catch {
    return null;
  } finally {
    _pollInFlight = false;
  }
}

async function loadInstances() {
  let data;
  try {
    const r = await fetch(`${BASE}/instances`);
    data = await r.json();
  } catch {
    return;
  }
  _activeId = data.active;
  applyInstances(data.instances);
  markActive(_activeId);
  if (data.active) {
    const activeInst = (data.instances || []).find(i => i.id === data.active);
    const termId = pickTerm(data.active, activeInst?.terms || []);
    connectTerminal(data.active, termId);
    updateProjectName(data.instances, data.active);
    loadTree(data.active);
  }
  setTimeout(() => _term?.focus(), 100);
}

function setProjectName(path) {
  const el = document.getElementById('project-name');
  const sep = document.querySelector('.pane-sep');
  if (path) {
    const parts = path.split(/[\\/]+/).filter(Boolean);
    const base = parts.pop() || path;
    if (el) {
      el.textContent = base;
      el.title = path;
      el.hidden = false;
    }
    if (sep) sep.hidden = false;
  } else {
    if (el) {
      el.textContent = '';
      el.title = '';
      el.hidden = true;
    }
    if (sep) sep.hidden = true;
  }
}

function updateProjectName(instances, activeId) {
  const inst = instances.find(i => i.id === activeId);
  setProjectName(inst ? inst.path : null);
}

async function addInstance() {
  let path;
  try {
    path = await window.pywebview.api.pick_folder();
  } catch {
    return;
  }
  if (!path) return;
  _listGen++;
  _listBusy++;
  try {
    const r = await fetch(`${BASE}/instances`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path }),
    });
    await r.json();
    await loadInstances();
  } catch {
    return;
  } finally {
    _listBusy--;
    _listGen++;
  }
}

async function activateInstance(id) {
  if (id === _activeId) return;
  if (!confirmDiscard()) return;
  try {
    await fetch(`${BASE}/instances/${id}/activate`, { method: 'POST' });
  } catch {
    return;
  }
  _activeId = id;
  markActive(id);
  const data = await pollInstances();
  if (data?.instances) {
    updateProjectName(data.instances, id);
  } else {
    const inst = _sessions.get(id);
    if (inst) updateProjectName([inst], id);
  }
  const inst = _sessions.get(id);
  const termId = pickTerm(id, inst?.terms || []);
  connectTerminal(id, termId);
  loadTree(id);
  resetEditor();
}

function normTermPath(raw, sessionPath = _sessions.get(_activeId)?.path) {
  let p = raw.replace(/:\d+(?::\d+)?$/, '').replace(/\\/g, '/').replace(/^\.\//, '');
  if (sessionPath) {
    const base = sessionPath.replace(/\\/g, '/').replace(/\/+$/, '');
    const prefix = base.toLowerCase() + '/';
    if (p.toLowerCase().startsWith(prefix)) {
      p = p.slice(prefix.length);
    }
  }
  return p;
}

// ── Phase 3: terminal via WebSocket ───────────────────────────────────
function initTerminal() {
  const mount = document.getElementById('terminal');
  if (typeof Terminal === 'undefined') {
    mount.textContent = 'xterm.js failed to load.';
    return;
  }
  const cs = getComputedStyle(document.documentElement);
  const tok = n => cs.getPropertyValue(n).trim();

  // Links open in the default Windows browser via Python; window.open in WebView2 hits the IE11 prompt.
  const openLink = (_e, url) => window.pywebview?.api?.open_url(url);

  _term = new Terminal({
    linkHandler: { activate: openLink }, // OSC 8 hyperlinks (http/https only by default)
    fontFamily: tok('--mono-font'),
    fontSize: 13,        // entero: evita texto "wavy" por redondeo sub-pixel del atlas WebGL
    lineHeight: 1.2,
    cursorBlink: true,
    scrollback: 10000,
    drawBoldTextInBrightColors: false,
    allowProposedApi: true, // xterm 6: required by the Unicode11 addon (_term.unicode), else it throws
    theme: {
      background:    tok('--panel'),
      foreground:    tok('--text'),
      cursor:        '#aeafad',
      selectionBackground: 'rgba(38,79,120,.6)',
      // ANSI 0-7: Windows Terminal defaults (compatible with PowerShell + PSReadLine)
      black:   '#0c0c0c',
      red:     '#c50f1f',
      green:   '#13a10e',
      yellow:  '#c19c00',
      blue:    '#0037da',
      magenta: '#881798',
      cyan:    '#3a96dd',   // PowerShell default prompt color (DarkCyan)
      white:   '#cccccc',
      // ANSI 8-15: bright variants
      brightBlack:   '#767676',
      brightRed:     '#e74856',
      brightGreen:   '#16c60c',
      brightYellow:  '#f9f1a5',
      brightBlue:    '#3b78ff',
      brightMagenta: '#b4009e',
      brightCyan:    '#61d6d6',
      brightWhite:   '#f2f2f2',
    },
  });

  if (window.Unicode11Addon) {
    try {
      const U11 = window.Unicode11Addon.Unicode11Addon || window.Unicode11Addon;
      _term.loadAddon(new U11());
      _term.unicode.activeVersion = '11';
    } catch (e) { console.warn('Unicode11 error:', e); }
  }

  if (window.FitAddon) {
    _fit = new window.FitAddon.FitAddon();
    _term.loadAddon(_fit);
  }
  if (window.WebLinksAddon) {
    _term.loadAddon(new window.WebLinksAddon.WebLinksAddon(openLink)); // plain-text URLs
  }
  _term.registerLinkProvider({
    provideLinks(y, callback) {
      if (_git.files.size === 0) {
        callback(undefined);
        return;
      }
      const line = _term.buffer.active.getLine(y - 1)?.translateToString(true);
      if (!line) {
        callback(undefined);
        return;
      }
      const re = /(?:[A-Za-z]:)?[\p{L}\p{N}_.\-\/\\]+\.[\p{L}\p{N}]+(?::\d+(?::\d+)?)?/gu;
      const links = [];
      let m;
      while ((m = re.exec(line))) {
        const surrounding = line.slice(Math.max(0, m.index - 3), m.index + m[0].length);
        if (surrounding.includes('://')) continue;
        const result = normTermPath(m[0]);
        if (!_git.files.has(result)) continue;
        links.push({
          range: {
            start: { x: m.index + 1, y },
            end: { x: m.index + m[0].length, y },
          },
          text: m[0],
          activate: () => openChange(result),
          decorations: { underline: true, pointerCursor: true },
        });
      }
      callback(links.length ? links : undefined);
    },
  });
  _term.open(mount);

  _term.attachCustomKeyEventHandler(e => {
    if (e.type !== 'keydown') return true;
    if (e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey) {
      const key = e.key.toLowerCase();
      if (key === 'c' && _term.hasSelection()) {
        navigator.clipboard.writeText(_term.getSelection());
        return false;
      }
    }
    if (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey && (e.key === 't' || e.key === 'T' || e.code === 'KeyT' || e.code === 'KeyP')) {
      return false;
    }
    return true;
  });

  if (window.WebglAddon) {
    try {
      const WebGL = window.WebglAddon.WebglAddon || window.WebglAddon;
      const _webgl = new WebGL();
      _webgl.onContextLoss(() => { _webgl.dispose(); });
      _term.loadAddon(_webgl);
    } catch (e) { console.warn('WebGL error:', e); }
  }

  // WebView2 congela rAF y timers cuando la ventana va a segundo plano.
  // Al volver al frente: reconectar si el WS murió y forzar re-render.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    scheduleGitRefresh();
    setTimeout(() => {
      if (_termInstId && (!_termWs || _termWs.readyState >= WebSocket.CLOSING)) {
        connectTerminal(_termInstId, _termId);
      }
      try { _fit?.fit(); } catch (_) {}
      try { _term?.refresh(0, _term.rows - 1); } catch (_) {}
    }, 100);
  });

  _term.onData(data => {
    // Block xterm.js device-attribute responses (Primary \x1b[?...c, Secondary \x1b[>...c,
    // Tertiary \x1b[=...c) from reaching PowerShell — PSReadLine corrupts on unexpected DA replies.
    if (/\x1b\[[\x3c\x3d\x3e\x3f]?[\d;]*c/.test(data)) return;

    if (_termWs?.readyState === WebSocket.OPEN) {
      _termWs.send(JSON.stringify({ type: 'data', data }));
    }
  });

  let _resizeTimer = null;
  _term.onResize(({ cols, rows }) => {
    clearTimeout(_resizeTimer);
    _resizeTimer = setTimeout(() => {
      if (_termWs?.readyState === WebSocket.OPEN) {
        _termWs.send(JSON.stringify({ type: 'resize', cols, rows }));
      }
    }, 50);
  });

  if (_fit) {
    try { _fit.fit(); } catch (e) { console.error('Fit error:', e); }
    new ResizeObserver(() => { try { _fit?.fit(); } catch (e) { } }).observe(mount);
  }

  let _copyTimeout = null;
  let _origStatusHtml = null;
  _term.onSelectionChange(() => {
    const sel = _term.getSelection();
    if (sel) {
      navigator.clipboard.writeText(sel).then(() => {
        const ws = document.getElementById('watch-status');
        if (ws) {
          if (_copyTimeout) {
            clearTimeout(_copyTimeout);
          } else {
            _origStatusHtml = ws.innerHTML;
          }
          ws.innerHTML = '<span style="color: #4ade80;">copied to clipboard</span>';
          _copyTimeout = setTimeout(() => {
            if (_origStatusHtml !== null) {
              ws.innerHTML = _origStatusHtml;
              _origStatusHtml = null;
            }
            _copyTimeout = null;
          }, 2000);
        }
      }).catch(() => {});
    }
  });

  document.getElementById('terminal').addEventListener('click', () => {
    _term?.focus();
  });
  document.getElementById('btn-add-term')?.addEventListener('click', () => {
    addTerminal();
  });
}

function pickTerm(instId, terms) {
  const remembered = _activeTerm.get(instId);
  const list = Array.isArray(terms) ? terms : [];
  if (remembered && list.includes(remembered)) {
    return remembered;
  }
  if (list.length > 0) {
    const sorted = [...list].sort();
    return sorted[0];
  }
  return '1';
}

function renderTermTabs(instId) {
  const tabsContainer = document.getElementById('term-tabs');
  const addBtn = document.getElementById('btn-add-term');
  const promptsBtn = document.getElementById('btn-prompts');
  if (!tabsContainer) return;

  if (!instId || instId !== _activeId) {
    tabsContainer.innerHTML = '';
    if (addBtn) addBtn.disabled = true;
    if (promptsBtn) promptsBtn.disabled = true;
    return;
  }

  const inst = _sessions.get(instId);
  const termsList = Array.isArray(inst?.terms) ? [...inst.terms] : [];
  if (_termId && !termsList.includes(_termId)) {
    termsList.push(_termId);
  }
  termsList.sort();

  if (addBtn) {
    addBtn.disabled = termsList.length >= 5;
  }
  if (promptsBtn) promptsBtn.disabled = false;

  tabsContainer.innerHTML = '';
  for (const tid of termsList) {
    const tab = document.createElement('div');
    tab.className = 'term-tab' + (tid === _termId ? ' active' : '');
    tab.dataset.tid = tid;

    const title = document.createElement('span');
    title.className = 'term-tab-title';
    title.textContent = `COM ${tid}`;
    tab.appendChild(title);

    const closeBtn = document.createElement('span');
    closeBtn.className = 'term-tab-close';
    closeBtn.title = 'Close terminal';
    closeBtn.textContent = '✕';
    closeBtn.addEventListener('click', e => {
      e.stopPropagation();
      closeTerminal(instId, tid);
    });
    tab.appendChild(closeBtn);

    tab.addEventListener('click', () => {
      if (tid === _termId) return;
      _activeTerm.set(instId, tid);
      connectTerminal(instId, tid);
    });

    tabsContainer.appendChild(tab);
  }
}

function addTerminal() {
  if (!_activeId) return;
  const inst = _sessions.get(_activeId);
  const termsList = Array.isArray(inst?.terms) ? inst.terms : [];
  const used = new Set(termsList);
  if (_termId) used.add(_termId);
  const freeId = ['1', '2', '3', '4', '5'].find(id => !used.has(id));
  if (!freeId) return;

  _activeTerm.set(_activeId, freeId);
  connectTerminal(_activeId, freeId);
}

async function closeTerminal(instId, tid) {
  const inst = _sessions.get(instId);
  if (inst && Array.isArray(inst.terms)) {
    inst.terms = inst.terms.filter(id => id !== tid);
  }
  _termScroll.delete(`${instId}:${tid}`);
  const wasActive = (instId === _activeId && tid === _termId);
  if (wasActive) {
    const nextTerm = pickTerm(instId, inst?.terms || []);
    if (nextTerm === tid) {
      try {
        await fetch(`${BASE}/instances/${instId}/terms/${tid}`, { method: 'DELETE' });
      } catch {}
      _activeTerm.set(instId, nextTerm);
      connectTerminal(instId, nextTerm);
      return;
    }
    _activeTerm.set(instId, nextTerm);
    connectTerminal(instId, nextTerm);
  } else {
    renderTermTabs(instId);
  }
  fetch(`${BASE}/instances/${instId}/terms/${tid}`, { method: 'DELETE' }).catch(() => {});
}

function connectTerminal(instId, termId) {
  if (!instId) return;
  if (!termId) {
    const inst = _sessions.get(instId);
    termId = pickTerm(instId, inst?.terms || []);
  }

  if (_termWs) {
    _termWs.onmessage = null;
    _termWs.close();
    _termWs = null;
  }
  if (!_term) return;

  const prevKey = (_termInstId && _termId) ? `${_termInstId}:${_termId}` : null;
  const newKey = `${instId}:${termId}`;
  const isFirstConnect = !prevKey || prevKey !== newKey;

  // Save scroll state of the current session/tab before switching
  if (prevKey && prevKey !== newKey) {
    const buf = _term.buffer.active;
    _termScroll.set(prevKey, {
      atBottom: buf.viewportY >= buf.baseY,
      viewportY: buf.viewportY,
    });
  }

  // Only reset the terminal on first connect (fresh session/tab), not on reconnect
  // to preserve alternate-screen state and scrollback.
  if (isFirstConnect) {
    _term.reset();
  }
  _termInstId = instId;
  _termId = termId;
  _activeTerm.set(instId, termId);

  const inst = _sessions.get(instId);
  if (inst) {
    if (!Array.isArray(inst.terms)) inst.terms = [];
    if (!inst.terms.includes(termId)) {
      inst.terms.push(termId);
      inst.terms.sort();
    }
  }
  renderTermTabs(instId);

  const ws = new WebSocket(`${WS}/ws/term/${instId}/${termId}`);
  _termWs = ws;

  let restorePending = true;

  ws.onmessage = ev => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === 'data') {
        if (restorePending) {
          restorePending = false;
          _term.write(msg.data, () => {
            const saved = _termScroll.get(newKey);
            if (saved && !saved.atBottom) {
              _term.scrollToLine(saved.viewportY);
            } else {
              _term.scrollToBottom();
            }
          });
        } else {
          // Sin batching: rAF se congela cuando WebView2 pasa a background y
          // acumulaba output sin renderizar. xterm.js difiere el sync del viewport
          // dentro de un bloque DEC 2026, así que escribir directo es seguro.
          _term.write(msg.data);
          const buf = _term.buffer.active;
          if (buf.viewportY >= buf.baseY) _term.scrollToBottom();
        }
      } else if (msg.type === 'agent') {
        pollInstances();
      } else if (msg.type === 'exit') {
        if (_termWs !== ws) return;
        ws.onclose = null;
        const currentInst = _sessions.get(instId);
        if (currentInst && Array.isArray(currentInst.terms)) {
          currentInst.terms = currentInst.terms.filter(tid => tid !== termId);
        }
        const nextTerm = pickTerm(instId, currentInst?.terms || []);
        connectTerminal(instId, nextTerm);
      }
    } catch { /* ignore */ }
  };

  ws.onopen = () => {
    if (_fit) _fit.fit();
    // Mandatory initial sync: the backend starts blind assuming 120x30.
    // Force it to adopt the exact frontend size to avoid mangling TUIs.
    ws.send(JSON.stringify({ type: 'resize', cols: _term.cols, rows: _term.rows }));
    _term.focus();
  };

  ws.onerror = () => {
    if (_termWs !== ws) return;
    _term?.writeln('\r\n\x1b[31m[connection lost]\x1b[0m');
  };

  ws.onclose = () => {
    if (_termWs !== ws) return;
    console.warn('[lango] term ws closed, reconnecting in 2s...');
    _term?.writeln('\r\n\x1b[33m[terminal disconnected]\x1b[0m');
    setTimeout(() => {
      if (_termInstId && _termWs === ws) {
        console.warn('[lango] term ws reconnecting');
        connectTerminal(_termInstId, _termId);
      }
    }, 2000);
  };
}

// ── Phase 5: file tree ────────────────────────────────────────────────

function fileIcon(name) {
  const ext = name.split('.').pop().toLowerCase();
  return {
    py: '#i-py', html: '#i-htm', htm: '#i-htm', css: '#i-css',
    js: '#i-js', jsx: '#i-js', ts: '#i-js', tsx: '#i-js',
    md: '#i-md', markdown: '#i-md'
  }[ext] || '#i-txt';
}

// Extensions that are "launched" on double-click / right-click (like Windows
// Explorer), without losing single-click to edit their source code.
const RUNNABLE_EXTS = new Set([
  'exe', 'com', 'msi', 'bat', 'cmd', 'vbs', 'vbe', 'ps1', 'lnk', 'wsf', 'jse',
]);

function isRunnable(name) {
  return RUNNABLE_EXTS.has(name.split('.').pop().toLowerCase());
}

function isHtml(name) {
  const ext = name.split('.').pop().toLowerCase();
  return ext === 'html' || ext === 'htm';
}

// Conecta los handlers de una fila de archivo: click simple → editor,
// doble click → ejecutar (si es ejecutable), botón derecho → menú contextual.
function wireFileRow(instId, entry, row) {
  row.addEventListener('click', e => {
    if (e.target.closest('.ctx-menu')) return;
    document.querySelectorAll('#tree .row.active').forEach(r => r.classList.remove('active'));
    row.classList.add('active');
    openChange(entry.path);
  });
  if (isRunnable(entry.name)) {
    row.classList.add('runnable');
    row.addEventListener('dblclick', e => {
      e.preventDefault();
      e.stopPropagation();
      launchFile(instId, entry.path);
    });
  }
  row.addEventListener('contextmenu', e => {
    e.preventDefault();
    showContextMenu(e, instId, entry);
  });
}

// Conecta los handlers de una fila de carpeta: click → expandir/colapsar,
// botón derecho → menú contextual.
function wireDirRow(instId, entry, row, childDepth) {
  row.addEventListener('click', e => {
    if (e.target.closest('.ctx-menu')) return;
    toggleDir(instId, entry.path, row, childDepth);
  });
  row.addEventListener('contextmenu', e => {
    e.preventDefault();
    showContextMenu(e, instId, entry);
  });
}

// ── File size tooltip ─────────────────────────────────────────────────
let _tipTimer = null;
let _tipEl = null;

function _getTipEl() {
  if (!_tipEl) {
    _tipEl = document.createElement('div');
    _tipEl.className = 'file-tip';
    document.body.appendChild(_tipEl);
  }
  return _tipEl;
}

function _showTip(text, x, y) {
  const el = _getTipEl();
  el.textContent = text;
  el.style.left = (x + 14) + 'px';
  el.style.top = (y - 6) + 'px';
  el.classList.add('visible');
}

function _hideTip() {
  clearTimeout(_tipTimer);
  _tipTimer = null;
  _tipEl?.classList.remove('visible');
}

function _formatSize(bytes) {
  if (bytes == null) return null;
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1048576) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1048576).toFixed(1)} MB`;
}

function makeTreeRow(entry, depth) {
  const row = document.createElement('div');
  row.className = 'row';
  row.style.setProperty('--depth', depth); // indent + guides per level (style.css), no cap
  row.dataset.path = entry.path;
  row.dataset.isDir = entry.is_dir ? '1' : '0';
  if (entry.is_dir) row.dataset.open = '0';

  let html = '';
  if (entry.is_dir) {
    // VS Code style: folders are chevron + name only (no folder icon)
    html += `<svg class="chev" viewBox="0 0 12 12"><use href="#i-chev"/></svg>`;
  } else {
    html += `<span class="chev"></span>`;
    html += `<svg class="ico-svg" viewBox="0 0 16 16"><use href="${fileIcon(entry.name)}"/></svg>`;
  }
  html += `<span class="fname"></span>`;
  row.innerHTML = html;
  row.querySelector('.fname').textContent = entry.name;

  const sizeStr = _formatSize(entry.size);
  if (sizeStr) {
    row.addEventListener('mouseenter', e => {
      _tipTimer = setTimeout(() => _showTip(sizeStr, e.clientX, e.clientY), 400);
    });
    row.addEventListener('mousemove', e => {
      if (_tipEl?.classList.contains('visible')) {
        _tipEl.style.left = (e.clientX + 14) + 'px';
        _tipEl.style.top = (e.clientY - 6) + 'px';
      }
    });
    row.addEventListener('mouseleave', _hideTip);
  }

  markRow(row);
  return row;
}

function rowByPath(path) {
  return document.querySelector(`#tree .row[data-path="${CSS.escape(path)}"]`);
}

function findSiblingInsertionPoint(tree, parentPath, name, isDir) {
  const prefix = parentPath ? parentPath + '/' : '';
  const rows = [...tree.querySelectorAll('.row[data-path]')];
  for (const r of rows) {
    const p = r.dataset.path || '';
    if (!p.startsWith(prefix)) continue;
    const suffix = p.slice(prefix.length);
    if (suffix.includes('/')) continue;
    const rIsDir = r.dataset.isDir === '1';
    if (isDir && !rIsDir) return r;
    if (!isDir && rIsDir) continue;
    const rName = r.querySelector('.fname')?.textContent || '';
    if (name.localeCompare(rName) < 0) return r;
  }
  return null;
}

function insertRowAfter(newRow, refRow) {
  const tree = document.getElementById('tree');
  if (refRow && refRow.nextSibling) {
    tree.insertBefore(newRow, refRow.nextSibling);
  } else {
    tree.appendChild(newRow);
  }
}

// Find the last DOM row that belongs to the subtree rooted at parentPath
function lastRowInSubtree(parentPath) {
  const all = [...document.querySelectorAll('#tree .row[data-path]')];
  const prefix = parentPath + '/';
  let last = rowByPath(parentPath);
  for (const row of all) {
    if (row.dataset.path.startsWith(prefix)) last = row;
  }
  return last;
}

function removeSubtree(parentPath) {
  const prefix = parentPath + '/';
  document.querySelectorAll('#tree .row[data-path]').forEach(row => {
    if (row.dataset.path.startsWith(prefix)) row.remove();
  });
}

async function loadTree(instId) {
  _treeInstId = instId;
  _expanded.clear();
  document.getElementById('tree').innerHTML = '';
  resetEditor();
  resetDiff();
  resetGit(instId);
  refreshGit();

  if (_filesWs) {
    _filesWs.close();
    _filesWs = null;
  }

  try {
    const r = await fetch(`${BASE}/tree?id=${encodeURIComponent(instId)}&path=`);
    if (_treeInstId !== instId) return;
    if (r.ok) {
      const entries = await r.json();
      if (_treeInstId !== instId) return;
      renderTreeRows(instId, entries, '', 0, null);
    }
  } catch (e) { console.warn(e); }

  connectFilesWs(instId);
  startReconcileSweep();
}

function renderTreeRows(instId, entries, parentPath, depth, afterRow) {
  const tree = document.getElementById('tree');
  let anchor = afterRow;

  for (const entry of entries) {
    const row = makeTreeRow(entry, depth);

    if (anchor && anchor.nextSibling) {
      tree.insertBefore(row, anchor.nextSibling);
    } else {
      tree.appendChild(row);
    }
    anchor = row;

    if (entry.is_dir) {
      wireDirRow(instId, entry, row, depth + 1);
    } else {
      wireFileRow(instId, entry, row);
    }
  }
}

async function toggleDir(instId, path, row, childDepth) {
  const open = row.dataset.open === '1';
  if (open) {
    row.dataset.open = '0';
    _expanded.set(path, false);
    removeSubtree(path);
  } else {
    row.dataset.open = '1';
    _expanded.set(path, true);
    try {
      const r = await fetch(`${BASE}/tree?id=${encodeURIComponent(instId)}&path=${encodeURIComponent(path)}`);
      if (!r.ok) return;
      const entries = await r.json();
      const lastChild = lastRowInSubtree(path);
      renderTreeRows(instId, entries, path, childDepth, lastChild === row ? row : lastChild);
    } catch (e) { console.warn(e); }
  }
}

function connectFilesWs(instId) {
  let retryDelay = 1000;
  let reconnected = false;
  const connect = () => {
    if (_treeInstId !== instId) return;
    const ws = new WebSocket(`${WS}/ws/files/${instId}`);
    _filesWs = ws;
    ws.onmessage = async ev => {
      try {
        const msg = JSON.parse(ev.data);
        await applyTreeEvent(instId, msg);
      } catch (e) { console.warn('ws onmessage', e); }
    };
    ws.onclose = () => {
      _filesWs = null;
      setLiveIndicator(false);
      if (_treeInstId !== instId) return;
      reconnected = true;
      setTimeout(connect, retryDelay);
      retryDelay = Math.min(retryDelay * 2, 30000);
    };
    ws.onopen = () => {
      retryDelay = 1000;
      setLiveIndicator(true);
      // Events fired while the socket was down are gone — reconcile every
      // open directory so the tree catches up to whatever changed offline.
      if (reconnected) {
        reconnected = false;
        reconcileDir(instId, '');
        for (const [path, open] of _expanded) {
          if (open === true) reconcileDir(instId, path);
        }
        scheduleGitRefresh();
      }
    };
  };
  connect();
}

function setLiveIndicator(connected) {
  const dot = document.querySelector('#watch-status .live-dot');
  if (dot) dot.style.color = connected ? '' : 'var(--danger)';
}

async function applyTreeEvent(instId, ev) {
  if (ev.kind !== 'root-gone' && instId === _activeId) scheduleGitRefresh();
  if (ev.kind === 'created') {
    // Only show if parent is expanded (or it's a root-level item)
    const parentPath = ev.path.includes('/') ? ev.path.split('/').slice(0, -1).join('/') : '';
    const parentExpanded = parentPath === '' || _expanded.get(parentPath) === true;
    if (!parentExpanded) return;
    if (rowByPath(ev.path)) return;  // already exists

    const name = ev.path.split('/').pop();
    const depth = ev.path.split('/').length - 1;
    const entry = { name, path: ev.path, is_dir: ev.is_dir, mtime: ev.mtime || 0, size: ev.size ?? null };

    const row = makeTreeRow(entry, depth);
    row.classList.add('appearing');
    const badge = document.createElement('span');
    badge.className = 'badge-new';
    row.appendChild(badge);
    setTimeout(() => badge.remove(), 3000);

    const tree = document.getElementById('tree');
    const before = findSiblingInsertionPoint(tree, parentPath, name, ev.is_dir);
    if (before) {
      tree.insertBefore(row, before);
    } else {
      insertRowAfter(row, lastRowInSubtree(parentPath));
    }

    if (ev.is_dir) {
      wireDirRow(instId, entry, row, depth + 1);
    } else {
      wireFileRow(instId, entry, row);
    }
  } else if (ev.kind === 'deleted') {
    const row = rowByPath(ev.path);
    if (row) {
      if (ev.is_dir) removeSubtree(ev.path);
      row.remove();
      _expanded.delete(ev.path);
    }
    if (_openInstId === instId && _openPath === ev.path) resetEditor();
    if (_change?.path === ev.path) {
      resetDiff();
      resetEditor();
    }
  } else if (ev.kind === 'moved') {
    const row = rowByPath(ev.path);
    if (row) {
      if (ev.is_dir) removeSubtree(ev.path);
      row.remove();
      _expanded.delete(ev.path);
    }
    if (_openInstId === instId && _openPath === ev.path) resetEditor();
    if (_change?.path === ev.path) {
      resetDiff();
      resetEditor();
    }

    const destParent = ev.dest.includes('/') ? ev.dest.split('/').slice(0, -1).join('/') : '';
    const parentExpanded = destParent === '' || _expanded.get(destParent) === true;
    if (parentExpanded && !rowByPath(ev.dest)) {
      const name = ev.dest.split('/').pop();
      const depth = ev.dest.split('/').length - 1;
      const entry = { name, path: ev.dest, is_dir: ev.is_dir, mtime: Date.now() / 1000 };
      const newRow = makeTreeRow(entry, depth);
      newRow.classList.add('appearing');
      const badge = document.createElement('span');
      badge.className = 'badge-new';
      newRow.appendChild(badge);
      setTimeout(() => badge.remove(), 3000);
      const tree = document.getElementById('tree');
      const before = findSiblingInsertionPoint(tree, destParent, name, ev.is_dir);
      if (before) {
        tree.insertBefore(newRow, before);
      } else {
        insertRowAfter(newRow, lastRowInSubtree(destParent));
      }
      if (ev.is_dir) {
        wireDirRow(instId, entry, newRow, depth + 1);
      } else {
        wireFileRow(instId, entry, newRow);
      }
    }
  } else if (ev.kind === 'dir-changed') {
    scheduleReconcile(instId, ev.path);
  } else if (ev.kind === 'root-gone') {
    handleRootGone(instId, ev.path);
  } else if (ev.kind === 'modified') {
    if (ev.path === _change?.path && _change.view === 'diff') {
      scheduleDiffReload();
    }
    if (_openInstId === instId && ev.path === _openPath && _editorView) {
      if (_dirty) {
        _diskChanged = true;
        updateEditorHeader();
      } else {
        const sel = _editorView.state.selection;
        const scrollTop = _editorView.scrollDOM.scrollTop;
        try {
          const r = await fetch(`${BASE}/file?id=${encodeURIComponent(_openInstId)}&path=${encodeURIComponent(_openPath)}`);
          if (!r.ok || _openPath !== ev.path) return;
          const info = await r.json();
          if (_dirty) {
            _diskChanged = true;
            updateEditorHeader();
            return;
          }
          _openMtime = info.mtime;
          _progSet = true;
          _editorView.dispatch({
            changes: { from: 0, to: _editorView.state.doc.length, insert: info.content },
            effects: _langComp?.reconfigure(await getLangExt(info.lang)),
            selection: { anchor: Math.min(sel.main.anchor, info.content.length), head: Math.min(sel.main.head, info.content.length) },
          });
          _progSet = false;
          _editorView.scrollDOM.scrollTop = scrollTop;
          updateStatusBar();
        } catch {}
      }
    }
  }
}

// ── Reconciliation: the safety net ─────────────────────────────────────
// Incremental created/deleted/moved events are fast but lossy on Windows
// (ReadDirectoryChangesW can drop events; "new folder" + rename can race).
// Reconciling re-reads a directory's real listing and patches the DOM to
// match, so the tree always converges to the true state on disk.

const _reconcileTimers = new Map();   // dirPath → timeout id (debounce)

function scheduleReconcile(instId, dirPath) {
  clearTimeout(_reconcileTimers.get(dirPath));
  _reconcileTimers.set(dirPath, setTimeout(() => {
    _reconcileTimers.delete(dirPath);
    reconcileDir(instId, dirPath);
  }, 150));
}

// Direct-child rows currently rendered under dirPath, keyed by full path.
function directChildRows(tree, dirPath) {
  const prefix = dirPath ? dirPath + '/' : '';
  const out = new Map();
  for (const r of tree.querySelectorAll('.row[data-path]')) {
    const p = r.dataset.path || '';
    if (!p.startsWith(prefix)) continue;
    const suffix = p.slice(prefix.length);
    if (suffix === '' || suffix.includes('/')) continue;
    out.set(p, r);
  }
  return out;
}

async function reconcileDir(instId, dirPath) {
  if (_treeInstId !== instId) return;
  // Root ('') is always visible; otherwise only reconcile expanded folders —
  // a row for a collapsed dir has no children rendered to reconcile.
  if (dirPath !== '' && _expanded.get(dirPath) !== true) return;
  if (dirPath !== '' && !rowByPath(dirPath)) return;  // parent no longer shown

  let entries;
  try {
    const r = await fetch(`${BASE}/tree?id=${encodeURIComponent(instId)}&path=${encodeURIComponent(dirPath)}`);
    if (!r.ok || _treeInstId !== instId) return;
    entries = await r.json();
  } catch (e) { return; }

  const tree = document.getElementById('tree');
  const existing = directChildRows(tree, dirPath);
  const wantPaths = new Set(entries.map(e => e.path));

  // Drop rows that no longer exist on disk (and their subtrees).
  for (const [p, row] of existing) {
    if (wantPaths.has(p)) continue;
    if (row.dataset.isDir === '1') removeSubtree(p);
    row.remove();
    _expanded.delete(p);
    if (_openInstId === instId && _openPath === p) resetEditor();
  }

  // Add rows present on disk but missing from the DOM.
  for (const entry of entries) {
    if (existing.has(entry.path)) continue;
    const depth = entry.path.split('/').length - 1;
    const row = makeTreeRow(entry, depth);
    const before = findSiblingInsertionPoint(tree, dirPath, entry.name, entry.is_dir);
    if (before) {
      tree.insertBefore(row, before);
    } else {
      insertRowAfter(row, lastRowInSubtree(dirPath));
    }
    if (entry.is_dir) wireDirRow(instId, entry, row, depth + 1);
    else wireFileRow(instId, entry, row);
  }
}

// Belt-and-suspenders: even if a dir-changed event is lost, sweep every
// open directory periodically so the tree can never drift permanently.
let _reconcileSweepTimer = null;

function startReconcileSweep() {
  stopReconcileSweep();
  _reconcileSweepTimer = setInterval(() => {
    if (!_treeInstId || document.hidden) return;
    reconcileDir(_treeInstId, '');
    for (const [path, open] of _expanded) {
      if (open === true) reconcileDir(_treeInstId, path);
    }
  }, 5000);
}

function stopReconcileSweep() {
  if (_reconcileSweepTimer) { clearInterval(_reconcileSweepTimer); _reconcileSweepTimer = null; }
}

// ── Phase 8: missing folder notification ───────────────────────────────

async function closeSession(id) {
  const actEl = document.querySelector(`#activitybar .act[data-id="${id}"]`);
  if (!actEl) return;

  _listGen++;
  _listBusy++;
  try {
    const wasActive = id === _activeId;
    _sessions.delete(id);
    _sessionOrder = _sessionOrder.filter(x => x !== id);
    actEl.remove();

    if (wasActive) {
      _activeId = null;
      _treeInstId = null;
      setProjectName(null);
      document.getElementById('tree').innerHTML = '';
      resetEditor();
      resetDiff();
      resetGit(null);
      if (_termWs) { _termWs.close(); _termWs = null; }
      if (_filesWs) { _filesWs.close(); _filesWs = null; }
      if (_term) { _term.reset(); }

      const remaining = document.querySelectorAll('#activitybar .act');
      if (remaining.length > 0) {
        const first = remaining[0];
        if (first.dataset.id) activateInstance(first.dataset.id);
      }
    }

    _activeTerm.delete(id);
    for (const key of _termScroll.keys()) {
      if (key.startsWith(`${id}:`)) _termScroll.delete(key);
    }
    _termScroll.delete(id);
    _gitCounts.delete(id);
    _changesFilterIds.delete(id);
    if (wasActive) {
      renderTermTabs(null);
    }

    try {
      await fetch(`${BASE}/instances/${id}`, { method: 'DELETE' });
    } catch (e) { console.warn(e); }
  } finally {
    _listBusy--;
    _listGen++;
  }
}

function handleRootGone(id, path) {
  closeSession(id);
  showNotification('error', `Not found: ${path}`);
}

function showNotification(type, message, timeout = 0) {
  const container = document.getElementById('notifications');
  if (!container) return;

  const banner = document.createElement('div');
  banner.className = 'notif-banner ' + type;

  const text = document.createElement('span');
  text.textContent = message;
  banner.appendChild(text);

  const closeBtn = document.createElement('span');
  closeBtn.className = 'notif-close';
  closeBtn.textContent = '\u2715';
  closeBtn.addEventListener('click', () => {
    banner.remove();
  });
  banner.appendChild(closeBtn);

  container.appendChild(banner);

  if (timeout > 0) setTimeout(() => banner.remove(), timeout);
}

// \u2500\u2500 Launch files + open external terminal \u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500

async function launchFile(instId, relPath) {
  const name = relPath.split('/').pop();
  try {
    const r = await fetch(`${BASE}/launch`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: instId, path: relPath }),
    });
    if (!r.ok) { showNotification('error', `Failed to run: ${name}`); return; }
    showNotification('info', `Running ${name}\u2026`, 2500);
  } catch (e) {
    showNotification('error', `Failed to run: ${name}`);
  }
}

async function openWindowsTerminal(instId) {
  try {
    const r = await fetch(`${BASE}/wt`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: instId }),
    });
    if (!r.ok) showNotification('error', 'Failed to open terminal.');
  } catch (e) {
    showNotification('error', 'Failed to open terminal.');
  }
}

// Doble click en una zona vacía del explorador → abrir consola externa.
function initEmptyAreaTerminal() {
  const sidebar = document.querySelector('.sidebar');
  if (!sidebar) return;
  sidebar.addEventListener('dblclick', e => {
    if (e.target.closest('.row')) return;          // un archivo/carpeta
    if (e.target.closest('.pane-head')) return;    // cabecera / drag region
    if (!_treeInstId) return;
    openWindowsTerminal(_treeInstId);
  });
  // Click derecho en zona vacía → menú contextual de la raíz del proyecto.
  sidebar.addEventListener('contextmenu', e => {
    if (e.target.closest('.row')) return;          // ya lo maneja la fila
    if (e.target.closest('.pane-head')) return;    // cabecera / drag region
    if (!_treeInstId) return;
    e.preventDefault();
    showContextMenu(e, _treeInstId, { isRoot: true, is_dir: true, path: '', name: '' });
  });
}

// ── Phase 6: editor ───────────────────────────────────────────────────

async function getLangExt(lang) {
  if (lang in _langCache) return _langCache[lang];
  let ext = [];
  try {
    switch (lang) {
      case 'python': {
        const m = await import('./vendor/codemirror/lang-python.js');
        ext = [m.python()]; break;
      }
      case 'javascript': {
        const m = await import('./vendor/codemirror/lang-javascript.js');
        ext = [m.javascript({ jsx: true })]; break;
      }
      case 'typescript': {
        const m = await import('./vendor/codemirror/lang-javascript.js');
        ext = [m.javascript({ typescript: true, jsx: true })]; break;
      }
      case 'css': {
        const m = await import('./vendor/codemirror/lang-css.js');
        ext = [m.css()]; break;
      }
      case 'html': {
        const m = await import('./vendor/codemirror/lang-html.js');
        ext = [m.html()]; break;
      }
      case 'markdown': {
        const m = await import('./vendor/codemirror/lang-markdown.js');
        ext = [m.markdown()]; break;
      }
      case 'json': {
        const m = await import('./vendor/codemirror/lang-json.js');
        ext = [m.json()]; break;
      }
    }
  } catch { ext = []; }
  _langCache[lang] = ext;
  return ext;
}

function updateWrapButton() {
  const btn = document.getElementById('btn-wrap');
  if (btn) {
    btn.classList.toggle('active', _wordWrap);
    btn.title = _wordWrap ? 'Disable Word Wrap (Alt+Z)' : 'Enable Word Wrap (Alt+Z)';
  }
}

function toggleWordWrap() {
  _wordWrap = !_wordWrap;
  localStorage.setItem('lango_word_wrap', _wordWrap);
  updateWrapButton();
  if (_editorView && _wrapComp && _EditorView) {
    _editorView.dispatch({ effects: _wrapComp.reconfigure(_wordWrap ? [_EditorView.lineWrapping] : []) });
  }
}

window.addEventListener('keydown', e => {
  if (e.altKey && (e.key === 'z' || e.key === 'Z' || e.code === 'KeyZ')) {
    if (e.target.closest('#terminal') || document.activeElement?.closest('#terminal')) return;
    e.preventDefault();
    toggleWordWrap();
  }
  if (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey && (e.key === 't' || e.key === 'T' || e.code === 'KeyT')) {
    e.preventDefault();
    addTerminal();
  }
  if (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey && e.code === 'KeyP') {
    e.preventDefault();
    if (palEl().hidden) openPalette(); else closePalette();
  }
});

async function initEditor() {
  const mount = document.getElementById('editor');
  const emptyEl = document.getElementById('editor-empty');
  if (emptyEl) emptyEl.style.display = 'grid';

  try {
    const [cmPkg, statePkg, langPkg, hlPkg] = await Promise.all([
      // Vendored bundle (esbuild, single @codemirror/state instance) — no CDN/network at startup
      import('./vendor/codemirror/codemirror.js'),
      import('./vendor/codemirror/state.js'),
      import('./vendor/codemirror/language.js'),
      import('./vendor/codemirror/highlight.js'),
    ]);
    const { EditorView, basicSetup } = cmPkg;
    _EditorView = EditorView;
    const { Compartment } = statePkg;
    const { HighlightStyle, syntaxHighlighting } = langPkg;
    const t = hlPkg.tags;
    _highlightCode = hlPkg.highlightCode;

    const cs = getComputedStyle(document.documentElement);
    const tok = n => cs.getPropertyValue(n).trim();

    const darkPlus = EditorView.theme({
      '&': { color: tok('--text'), backgroundColor: tok('--editor'), height: '100%', fontSize: '13px' },
      '.cm-content': { fontFamily: tok('--mono-font'), caretColor: tok('--accent') },
      '.cm-gutters': { backgroundColor: tok('--editor'), color: tok('--text-faint'), border: 'none' },
      '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,.04)' },
      '.cm-activeLineGutter': { backgroundColor: 'transparent', color: tok('--text') },
      '.cm-cursor': { borderLeftColor: tok('--accent') },
      '.cm-selectionBackground, &.cm-focused .cm-selectionBackground':
        { backgroundColor: 'rgba(38,79,120,.6)' },
    }, { dark: true });

    const highlight = HighlightStyle.define([
      { tag: [t.keyword, t.controlKeyword, t.moduleKeyword], color: tok('--sx-keyword') },
      { tag: [t.string, t.special(t.string)], color: tok('--sx-string') },
      { tag: [t.function(t.variableName), t.function(t.propertyName)], color: tok('--sx-func') },
      { tag: [t.comment, t.lineComment, t.blockComment], color: tok('--sx-comment'), fontStyle: 'italic' },
      { tag: [t.number, t.bool], color: tok('--sx-number') },
      { tag: [t.typeName, t.className, t.namespace], color: tok('--sx-type') },
      { tag: [t.variableName, t.propertyName, t.attributeName], color: tok('--sx-var') },
      { tag: [t.meta, t.annotation], color: tok('--sx-deco') },
      { tag: [t.operator, t.punctuation], color: tok('--text') },
    ]);
    _hlStyle = highlight;

    _langComp = new Compartment();
    _wrapComp = new Compartment();

    _editorView = new EditorView({
      doc: '',
      parent: mount,
      extensions: [
        basicSetup,
        darkPlus,
        syntaxHighlighting(highlight),
        _langComp.of([]),
        _wrapComp.of(_wordWrap ? [_EditorView.lineWrapping] : []),
        EditorView.updateListener.of(update => {
          if (update.docChanged && !_progSet) markDirty();
        }),
      ],
    });

    window._editor = _editorView;  // debug access
    if (emptyEl) emptyEl.style.display = 'none';
  } catch (err) {
    console.warn('CodeMirror no disponible:', err);
  }

  // Ctrl+S — capture before CodeMirror
  document.getElementById('editor').addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      e.stopPropagation();
      if (_openPath && _openInstId) saveFile(_openInstId, _openPath);
    }
  }, true);

  document.getElementById('btn-save')?.addEventListener('click', () => {
    if (_openPath && _openInstId) saveFile(_openInstId, _openPath);
  });
  document.getElementById('btn-reload')?.addEventListener('click', reloadFile);
  document.getElementById('btn-wrap')?.addEventListener('click', toggleWordWrap);
}

function confirmDiscard() {
  if (!_dirty || !_openPath) return true;
  const name = _openPath.split('/').pop();
  const msg = _diskChanged
    ? `"${name}" has local edits and disk changes. Discard local edits?`
    : `"${name}" has unsaved changes. Discard them?`;
  return confirm(msg);
}

async function openFile(instId, relPath) {
  if (!_editorView) return false;
  if (!confirmDiscard()) return false;
  try {
    const r = await fetch(`${BASE}/file?id=${encodeURIComponent(instId)}&path=${encodeURIComponent(relPath)}`);
    if (!r.ok) return false;
    const info = await r.json();

    _openInstId = instId;
    _openPath = relPath;
    _openMtime = info.mtime;
    _dirty = false;
    _diskChanged = false;

    const langExt = await getLangExt(info.lang);
    _progSet = true;
    _editorView.dispatch({
      changes: { from: 0, to: _editorView.state.doc.length, insert: info.content },
      effects: _langComp?.reconfigure(langExt),
    });
    _progSet = false;

    updateEmptyState();
    updateStatusBar();
    updateEditorHeader();
    return true;
  } catch (e) {
    console.warn(e);
    return false;
  }
}

async function openPrompt(path) {
  if (!await openFile(PROMPTS_ID, path)) return;
  resetDiff();
  document.querySelectorAll('#tree .row.active, #tree-changes .row.active').forEach(r => r.classList.remove('active'));
  _editorView.focus();
}

async function saveFile(instId, relPath) {
  if (!_editorView || !_dirty) return;
  if (_diskChanged) {
    showNotification('warn', 'File changed on disk — reload before saving.', 3000);
    return;
  }
  const content = _editorView.state.doc.toString();
  let body = { id: instId, path: relPath, content, mtime: _openMtime };
  try {
    let r = await fetch(`${BASE}/file`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (r.status === 409) {
      if (!confirm('The file changed on disk since you opened it. Overwrite?')) return;
      body = { ...body, force: true };
      r = await fetch(`${BASE}/file`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
    }
    if (!r.ok) return;
    const info = await r.json();
    if (info.ok) {
      _dirty = false;
      _diskChanged = false;
      _openMtime = info.mtime;
      updateStatusBar();
      updateEditorHeader();
      scheduleGitRefresh();
    }
  } catch (e) { console.warn(e); }
}

async function reloadFile() {
  if (!_openPath || !_openInstId || !_editorView) return;
  if (_dirty && !confirm(`Discard changes to "${_openPath.split('/').pop()}"?`)) return;

  try {
    const r = await fetch(`${BASE}/file?id=${encodeURIComponent(_openInstId)}&path=${encodeURIComponent(_openPath)}`);
    if (!r.ok) return;
    const info = await r.json();

    const sel = _editorView.state.selection;
    const scrollTop = _editorView.scrollDOM.scrollTop;

    _openMtime = info.mtime;
    _dirty = false;
    _diskChanged = false;

    _progSet = true;
    _editorView.dispatch({
      changes: { from: 0, to: _editorView.state.doc.length, insert: info.content },
      effects: _langComp?.reconfigure(await getLangExt(info.lang)),
      selection: {
        anchor: Math.min(sel.main.anchor, info.content.length),
        head: Math.min(sel.main.head, info.content.length),
      },
    });
    _progSet = false;
    _editorView.scrollDOM.scrollTop = scrollTop;

    updateStatusBar();
    updateEditorHeader();
  } catch (e) { console.warn(e); }
}

function resetEditor() {
  _openInstId = null;
  _openPath = null;
  _openMtime = null;
  _dirty = false;
  _diskChanged = false;
  if (_editorView) {
    _progSet = true;
    _editorView.dispatch({
      changes: { from: 0, to: _editorView.state.doc.length, insert: '' },
      effects: _langComp?.reconfigure([]),
    });
    _progSet = false;
  }
  updateEmptyState();
  document.getElementById('file-meta').textContent = '';
  updateEditorHeader();
}

function updateEmptyState() {
  const emptyEl = document.getElementById('editor-empty');
  const homeEl = document.getElementById('changes-home');
  if (_openPath || _change) {
    if (emptyEl) emptyEl.style.display = 'none';
    if (homeEl) homeEl.hidden = true;
  } else if (_git.repo && _git.total > 0) {
    if (emptyEl) emptyEl.style.display = 'none';
    if (homeEl) {
      homeEl.hidden = false;
      renderChangesHome();
    }
  } else {
    if (emptyEl) emptyEl.style.display = 'grid';
    if (homeEl) {
      homeEl.hidden = true;
      homeEl.replaceChildren();
    }
  }
}

function renderChangesHome() {
  const homeEl = document.getElementById('changes-home');
  if (!homeEl) return;
  homeEl.replaceChildren();

  const activeInst = _sessions.get(_activeId);
  const proj = activeInst ? projectName(activeInst) : '';
  const h2 = document.createElement('h2');
  h2.textContent = proj ? `Changes in ${proj}` : 'Changes';
  homeEl.appendChild(h2);

  const p = document.createElement('p');
  const n = _git.total;
  const countText = `${n} ${n === 1 ? 'file' : 'files'}`;
  const refText = _git.detached ? `HEAD ${_git.branch}` : `branch ${_git.branch}`;
  p.textContent = `${countText} since the last commit · ${refText}`;
  homeEl.appendChild(p);

  const sortedPaths = [..._git.files.keys()].sort((a, b) => a.localeCompare(b));
  for (const path of sortedPaths) {
    const info = _git.files.get(path);
    if (!info) continue;

    const row = document.createElement('div');
    row.className = 'ch-item';
    row.dataset.file = path;

    const pill = document.createElement('span');
    pill.className = `git-pill ${info.letter}`;
    pill.textContent = info.letter;
    row.appendChild(pill);

    const slashIdx = path.lastIndexOf('/');
    const dir = slashIdx === -1 ? '' : path.slice(0, slashIdx + 1);
    const name = slashIdx === -1 ? path : path.slice(slashIdx + 1);

    const iconSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    iconSvg.setAttribute('class', 'ico-svg');
    iconSvg.setAttribute('viewBox', '0 0 16 16');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', fileIcon(name));
    iconSvg.appendChild(use);
    row.appendChild(iconSvg);

    const pSpan = document.createElement('span');
    pSpan.className = 'p';
    if (dir) {
      pSpan.appendChild(document.createTextNode(dir));
    }
    const b = document.createElement('b');
    b.textContent = name;
    pSpan.appendChild(b);
    row.appendChild(pSpan);

    if (info.add !== -1) {
      const stat = document.createElement('span');
      stat.className = 'stat';
      if (info.letter === 'D') {
        const dSpan = document.createElement('span');
        dSpan.className = 'd';
        dSpan.textContent = `\u2212${info.del}`;
        stat.appendChild(dSpan);
      } else {
        const aSpan = document.createElement('span');
        aSpan.className = 'a';
        aSpan.textContent = `+${info.add}`;
        const dSpan = document.createElement('span');
        dSpan.className = 'd';
        dSpan.textContent = `\u2212${info.del}`;
        stat.appendChild(aSpan);
        stat.appendChild(dSpan);
      }
      row.appendChild(stat);
    }

    row.addEventListener('click', () => {
      openChange(path);
    });

    homeEl.appendChild(row);
  }

  if (_git.truncated) {
    const more = document.createElement('div');
    more.className = 'ch-item ch-more';
    more.textContent = 'and more…';
    homeEl.appendChild(more);
  }
}

function markDirty() {
  if (_dirty) return;
  _dirty = true;
  updateStatusBar();
  updateEditorHeader();
}

function updateStatusBar() {
  const el = document.getElementById('file-meta');
  if (!_openPath) { el.textContent = ''; return; }
  const lines = _editorView ? _editorView.state.doc.lines : 0;
  const dirty = _dirty ? ' ● ' : '';
  let ago = '';
  if (_openMtime) {
    const secs = (Date.now() / 1000 | 0) - _openMtime;
    ago = secs < 60 ? ` · ${secs}s ago`
      : secs < 3600 ? ` · ${Math.floor(secs / 60)}m ago`
        : ` · ${Math.floor(secs / 3600)}h ago`;
  }
  const shown = _openInstId === PROMPTS_ID ? `prompts/${_openPath}` : _openPath;
  el.textContent = `${shown}${dirty} · ${lines} ${lines === 1 ? 'line' : 'lines'}${ago}`;
}

// ── Git state & status bar ────────────────────────────────────────────

function markRow(row) {
  if (!row) return;
  row.querySelectorAll('.git-letter, .git-dot').forEach(el => el.remove());
  delete row.dataset.git;

  const path = row.dataset.path;
  if (!path) return;

  if (row.dataset.isDir === '0') {
    const f = _git.files?.get(path);
    if (f) {
      row.dataset.git = f.letter;
      const span = document.createElement('span');
      span.className = 'git-letter';
      span.textContent = f.letter;
      row.appendChild(span);
    }
  } else if (row.dataset.isDir === '1') {
    if (_git.dirs?.has(path)) {
      const dot = document.createElement('span');
      dot.className = 'git-dot';
      row.appendChild(dot);
    }
  }
}

function applyGitMarks() {
  document.querySelectorAll('#tree .row').forEach(markRow);
  renderChangesFilter();
  updateEmptyState();
}

function renderChangesFilter() {
  const btn = document.getElementById('btn-changes');
  const countEl = document.getElementById('changes-count');
  const tree = document.getElementById('tree');
  const treeChanges = document.getElementById('tree-changes');
  if (!btn || !tree || !treeChanges) return;

  const shouldHide = !_git.repo || _git.total === 0;
  btn.hidden = shouldHide;
  if (shouldHide && _git.ready && _git.instId === _activeId) {
    _changesFilterIds.delete(_activeId);
  }
  const filterOn = changesFilterOn() && !shouldHide;

  if (countEl) {
    countEl.textContent = _git.truncated ? '500+' : String(_git.total);
  }
  btn.classList.toggle('on', filterOn);

  if (filterOn) {
    tree.hidden = true;
    treeChanges.hidden = false;
    treeChanges.replaceChildren();

    const sortedPaths = [..._git.files.keys()].sort((a, b) => a.localeCompare(b));
    for (const path of sortedPaths) {
      const info = _git.files.get(path);
      if (!info) continue;

      const row = document.createElement('div');
      row.className = 'row';
      row.dataset.path = path;
      row.dataset.isDir = '0';
      row.dataset.git = info.letter;
      if (_openPath === path) {
        row.classList.add('active');
      }

      const chev = document.createElement('span');
      chev.className = 'chev';
      row.appendChild(chev);

      const slashIdx = path.lastIndexOf('/');
      const folder = slashIdx === -1 ? '' : path.slice(0, slashIdx);
      const name = slashIdx === -1 ? path : path.slice(slashIdx + 1);

      const iconSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      iconSvg.setAttribute('class', 'ico-svg');
      iconSvg.setAttribute('viewBox', '0 0 16 16');
      const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
      use.setAttribute('href', fileIcon(name));
      iconSvg.appendChild(use);
      row.appendChild(iconSvg);

      const nameSpan = document.createElement('span');
      nameSpan.className = 'fname';
      nameSpan.textContent = name;
      row.appendChild(nameSpan);

      if (folder) {
        const dirSpan = document.createElement('span');
        dirSpan.className = 'dir-hint';
        dirSpan.textContent = folder;
        row.appendChild(dirSpan);
      }

      const letterSpan = document.createElement('span');
      letterSpan.className = 'git-letter';
      letterSpan.textContent = info.letter;
      row.appendChild(letterSpan);

      row.addEventListener('click', () => {
        document.querySelectorAll('#tree-changes .row.active').forEach(r => r.classList.remove('active'));
        row.classList.add('active');
        openChange(path);
      });

      treeChanges.appendChild(row);
    }
  } else {
    tree.hidden = false;
    treeChanges.hidden = true;
    treeChanges.replaceChildren();
  }
}

function renderStatusGit() {
  const branchEl = document.getElementById('git-branch');
  if (branchEl) {
    branchEl.hidden = !_git.repo;
    const nameEl = branchEl.querySelector('span');
    if (nameEl) nameEl.textContent = _git.branch || '';
  }

  const countEl = document.getElementById('git-count');
  if (countEl) {
    countEl.hidden = !_git.repo;
    countEl.classList.toggle('chg', _git.total > 0);
    if (_git.total === 0) {
      countEl.textContent = 'No changes';
    } else if (_git.truncated) {
      countEl.textContent = '500+ changes';
    } else {
      const n = _git.total;
      countEl.textContent = `${n} ${n === 1 ? 'change' : 'changes'}`;
    }
  }
}

async function refreshGit() {
  if (!_activeId) return;
  if (_gitCtl) {
    _gitPending = true;
    return;
  }
  const ctl = new AbortController();
  _gitCtl = ctl;
  const gen = _gitGen;
  const instId = _activeId;
  console.debug('git refresh', instId);

  try {
    let data;
    try {
      const res = await fetch(`${BASE}/git/status?id=${encodeURIComponent(instId)}`, { signal: ctl.signal });
      if (!res.ok) {
        data = { repo: false };
      } else {
        data = await res.json();
      }
    } catch {
      data = { repo: false };
    }

    if (gen !== _gitGen || instId !== _activeId) {
      return;
    }

    const files = new Map();
    const dirs = new Set();
    const isRepo = Boolean(data && data.repo);

    if (isRepo && Array.isArray(data.files)) {
      for (const f of data.files) {
        if (!f || !f.path) continue;
        files.set(f.path, { letter: f.letter, add: f.add, del: f.del });
        const parts = f.path.replace(/\\/g, '/').split('/');
        let cur = '';
        for (let i = 0; i < parts.length - 1; i++) {
          cur = cur ? `${cur}/${parts[i]}` : parts[i];
          if (cur) dirs.add(cur);
        }
      }
    }

    _gitCounts.set(instId, {
      total: isRepo ? (typeof data.total === 'number' ? data.total : files.size) : 0,
      truncated: Boolean(isRepo && data.truncated),
    });
    _git = {
      instId,
      ready: true,
      repo: isRepo,
      branch: (isRepo && data.branch) || '',
      detached: Boolean(isRepo && data.detached),
      truncated: Boolean(isRepo && data.truncated),
      total: isRepo ? (typeof data.total === 'number' ? data.total : files.size) : 0,
      files,
      dirs,
    };

    renderStatusGit();
    renderSessions();
    applyGitMarks();
  } finally {
    if (_gitCtl === ctl) {
      _gitCtl = null;
      if (_gitPending) {
        _gitPending = false;
        refreshGit();
      }
    }
  }
}

function clearGitTimers() {
  if (_gitDebounceTimer) {
    clearTimeout(_gitDebounceTimer);
    _gitDebounceTimer = null;
  }
  if (_gitMaxTimer) {
    clearTimeout(_gitMaxTimer);
    _gitMaxTimer = null;
  }
  _gitFirstScheduled = 0;
}

function scheduleGitRefresh() {
  const now = Date.now();
  if (_gitFirstScheduled && now - _gitFirstScheduled >= 2000) {
    clearGitTimers();
    refreshGit();
    return;
  }
  if (!_gitFirstScheduled) {
    _gitFirstScheduled = now;
    _gitMaxTimer = setTimeout(() => {
      clearGitTimers();
      refreshGit();
    }, 2000);
  }
  if (_gitDebounceTimer) {
    clearTimeout(_gitDebounceTimer);
  }
  _gitDebounceTimer = setTimeout(() => {
    clearGitTimers();
    refreshGit();
  }, 500);
}

function resetGit(instId) {
  _gitGen++;
  _gitCtl?.abort();
  _gitCtl = null;
  clearGitTimers();
  _gitPending = false;
  _git = {
    instId: instId ?? null,
    ready: false,
    repo: false,
    branch: '',
    detached: false,
    truncated: false,
    total: 0,
    files: new Map(),
    dirs: new Set(),
  };
  renderStatusGit();
  renderSessions();
  applyGitMarks();
}

async function openChange(path) {
  const gitInfo = _git.files?.get(path);
  if (!gitInfo) {
    if (await openFile(_treeInstId, path)) resetDiff();
    return;
  }
  if (!confirmDiscard()) return;
  resetEditor();
  _change = { path, letter: gitInfo.letter, view: 'diff', data: null, idx: 0 };
  await loadDiff();
  document.getElementById('diff-view')?.focus();
}

async function loadDiff({ keepScroll = false } = {}) {
  if (!_change) return;
  const currentChange = _change;
  const path = currentChange.path;
  const instId = _treeInstId;
  const diffView = document.getElementById('diff-view');
  const prevScrollTop = (keepScroll && diffView) ? diffView.scrollTop : 0;

  let data = null;
  try {
    const res = await fetch(`${BASE}/git/diff?id=${encodeURIComponent(instId)}&path=${encodeURIComponent(path)}`);
    if (res.ok) {
      data = await res.json();
    }
  } catch (e) {
    console.warn('loadDiff fetch error', e);
  }

  if (_change !== currentChange) return;

  _change.data = data;
  if (data?.letter) {
    _change.letter = data.letter;
  }
  const hunkCount = data?.hunks ? data.hunks.length : 0;
  if (hunkCount === 0) {
    _change.idx = 0;
  } else {
    _change.idx = Math.min(Math.max(0, _change.idx), hunkCount - 1);
  }

  await renderDiff();
  if (_change !== currentChange) return;

  renderChangeCtl();

  if (keepScroll && diffView) {
    diffView.scrollTop = prevScrollTop;
  }
}

async function renderDiff() {
  const diffView = document.getElementById('diff-view');
  if (!diffView || !_change) return;
  const currentChange = _change;
  diffView.replaceChildren();

  const data = _change.data;
  if (!data) return;

  if (data.binary || data.too_large || !data.hunks || data.hunks.length === 0) {
    const msgBox = document.createElement('div');
    msgBox.className = 'diff-msg';
    const text = document.createElement('div');
    if (data.binary) {
      text.textContent = 'Binary file';
    } else if (data.too_large) {
      text.textContent = 'Diff too large';
    } else {
      text.textContent = 'No changes';
    }
    msgBox.appendChild(text);

    if (_change.letter !== 'D') {
      const btn = document.createElement('button');
      btn.textContent = 'Open file';
      btn.addEventListener('click', () => {
        switchView('file');
      });
      msgBox.appendChild(btn);
    }
    diffView.appendChild(msgBox);
    return;
  }

  let parser = null;
  try {
    const langExt = await getLangExt(data.lang);
    parser = langExt[0]?.language?.parser;
  } catch (e) {
    parser = null;
  }

  if (_change !== currentChange) return;

  const canHighlight = Boolean(parser && _highlightCode && _hlStyle);

  data.hunks.forEach((hunk, hunkIdx) => {
    const hunkEl = document.createElement('div');
    hunkEl.className = 'hunk' + (hunkIdx === _change.idx ? ' cur' : '');
    hunkEl.textContent = hunk.header;
    hunkEl.addEventListener('click', () => {
      selectHunk(hunkIdx);
    });
    diffView.appendChild(hunkEl);

    let oldLinesSide = null;
    let newLinesSide = null;
    let useHighlightForHunk = false;

    if (canHighlight) {
      try {
        const newLines = hunk.lines.filter(l => l[0] === ' ' || l[0] === '+');
        const oldLines = hunk.lines.filter(l => l[0] === ' ' || l[0] === '-');
        const newText = newLines.map(l => l[1]).join('\n');
        const oldText = oldLines.map(l => l[1]).join('\n');

        newLinesSide = [[]];
        if (newLines.length > 0) {
          _highlightCode(
            newText,
            parser.parse(newText),
            _hlStyle,
            (t, cls) => {
              const span = document.createElement('span');
              span.textContent = t;
              if (cls) span.className = cls;
              newLinesSide[newLinesSide.length - 1].push(span);
            },
            () => {
              newLinesSide.push([]);
            }
          );
        }

        oldLinesSide = [[]];
        if (oldLines.length > 0) {
          _highlightCode(
            oldText,
            parser.parse(oldText),
            _hlStyle,
            (t, cls) => {
              const span = document.createElement('span');
              span.textContent = t;
              if (cls) span.className = cls;
              oldLinesSide[oldLinesSide.length - 1].push(span);
            },
            () => {
              oldLinesSide.push([]);
            }
          );
        }
        useHighlightForHunk = true;
      } catch (e) {
        console.warn('diff highlight', e);
        useHighlightForHunk = false;
      }
    }

    let oldNo = hunk.old_start;
    let newNo = hunk.new_start;
    let newSideIdx = 0;
    let oldSideIdx = 0;

    for (const [sign, text] of hunk.lines) {
      const row = document.createElement('div');
      row.className = 'ln' + (sign === '+' ? ' add' : sign === '-' ? ' del' : '');

      const bar = document.createElement('span');
      bar.className = 'bar';
      row.appendChild(bar);

      let oldNoStr = '';
      let newNoStr = '';
      if (sign === '+') {
        newNoStr = String(newNo++);
      } else if (sign === '-') {
        oldNoStr = String(oldNo++);
      } else {
        oldNoStr = String(oldNo++);
        newNoStr = String(newNo++);
      }

      const noOld = document.createElement('span');
      noOld.className = 'no';
      noOld.textContent = oldNoStr;
      row.appendChild(noOld);

      const noNew = document.createElement('span');
      noNew.className = 'no';
      noNew.textContent = newNoStr;
      row.appendChild(noNew);

      const signEl = document.createElement('span');
      signEl.className = 'sign';
      signEl.textContent = sign === ' ' ? '' : sign;
      row.appendChild(signEl);

      const txtEl = document.createElement('span');
      txtEl.className = 'txt';

      if (useHighlightForHunk) {
        const sideLines = sign === '-' ? oldLinesSide : newLinesSide;
        const currentSideIdx = sign === '-' ? oldSideIdx++ : newSideIdx++;
        const spans = sideLines?.[currentSideIdx];
        if (spans && spans.length > 0) {
          for (const s of spans) {
            txtEl.appendChild(s);
          }
        } else if (!spans) {
          txtEl.textContent = text;
        }
      } else {
        txtEl.textContent = text;
      }

      row.appendChild(txtEl);
      diffView.appendChild(row);
    }
  });
}

function selectHunk(idx, scroll = true) {
  if (!_change?.data?.hunks) return;
  const n = _change.data.hunks.length;
  if (n === 0) return;
  _change.idx = ((idx % n) + n) % n;
  const hunks = document.querySelectorAll('#diff-view .hunk');
  hunks.forEach((el, i) => {
    el.classList.toggle('cur', i === _change.idx);
  });
  if (scroll && hunks[_change.idx]) {
    hunks[_change.idx].scrollIntoView({ block: 'start' });
  }
  const hunkNav = document.getElementById('hunk-nav');
  if (hunkNav) {
    hunkNav.textContent = `${_change.idx + 1}/${n}`;
  }
}

function renderChangeCtl() {
  const ctl = document.getElementById('change-ctl');
  const pathEl = document.getElementById('change-path');
  const pillEl = document.getElementById('change-pill');
  const navEl = document.getElementById('change-nav');
  const hunkNavEl = document.getElementById('hunk-nav');
  const noteEl = document.getElementById('change-note');
  const diffView = document.getElementById('diff-view');
  const btnWrap = document.getElementById('btn-wrap');
  const btnReload = document.getElementById('btn-reload');
  const btnSave = document.getElementById('btn-save');
  const badge = document.getElementById('editor-badge');
  const segButtons = ctl?.querySelectorAll('.seg-ctl button');

  if (!_change) {
    if (ctl) ctl.hidden = true;
    if (diffView) diffView.hidden = true;
    if (btnWrap) btnWrap.hidden = false;
    if (btnReload) btnReload.hidden = false;
    if (btnSave) btnSave.hidden = false;
    const btnCloseFile = document.getElementById('btn-close-file');
    if (btnCloseFile) btnCloseFile.hidden = false;
    if (badge) badge.hidden = false;
    updateEmptyState();
    return;
  }

  if (ctl) ctl.hidden = false;

  if (pathEl) {
    pathEl.replaceChildren();
    const slashIdx = _change.path.lastIndexOf('/');
    const dir = slashIdx === -1 ? '' : _change.path.slice(0, slashIdx + 1);
    const name = slashIdx === -1 ? _change.path : _change.path.slice(slashIdx + 1);
    if (dir) pathEl.appendChild(document.createTextNode(dir));
    const b = document.createElement('b');
    b.textContent = name;
    pathEl.appendChild(b);
  }

  if (pillEl) {
    pillEl.className = `git-pill ${_change.letter || ''}`;
    pillEl.textContent = _change.letter || '';
  }

  if (segButtons) {
    segButtons.forEach(btn => {
      const v = btn.dataset.view;
      btn.classList.toggle('on', v === _change.view);
      if (v === 'file') {
        btn.disabled = (_change.letter === 'D');
      }
    });
  }

  const hunks = _change.data?.hunks;
  const hunkCount = hunks ? hunks.length : 0;
  const isDiff = _change.view === 'diff';

  if (navEl) {
    navEl.hidden = !(isDiff && hunkCount >= 1);
    if (hunkNavEl && isDiff && hunkCount >= 1) {
      hunkNavEl.textContent = `${_change.idx + 1}/${hunkCount}`;
    }
  }

  if (noteEl) {
    noteEl.hidden = !(isDiff && _dirty && _openPath === _change.path);
  }

  if (btnWrap) btnWrap.hidden = isDiff;
  if (btnReload) btnReload.hidden = isDiff;
  if (btnSave) btnSave.hidden = isDiff;
  const btnCloseFile = document.getElementById('btn-close-file');
  if (btnCloseFile) btnCloseFile.hidden = isDiff;
  if (badge) badge.hidden = isDiff;

  if (diffView) {
    diffView.hidden = !isDiff;
  }

  updateEmptyState();
}

async function switchView(targetView) {
  if (!_change || _change.view === targetView) return;
  if (targetView === 'file') {
    if (_change.letter === 'D') return;
    _change.view = 'file';
    if (_openPath === _change.path) {
      renderChangeCtl();
    } else {
      const ok = await openFile(_treeInstId, _change.path);
      if (!ok) {
        _change.view = 'diff';
        renderChangeCtl();
        return;
      }
      renderChangeCtl();
    }
  } else if (targetView === 'diff') {
    _change.view = 'diff';
    await loadDiff();
    document.getElementById('diff-view')?.focus();
  }
}

function closeChange() {
  if (!confirmDiscard()) return;
  resetDiff();
  resetEditor();
}

function resetDiff() {
  if (_diffDebounceTimer) {
    clearTimeout(_diffDebounceTimer);
    _diffDebounceTimer = null;
  }
  _change = null;
  const diffView = document.getElementById('diff-view');
  if (diffView) {
    diffView.hidden = true;
    diffView.replaceChildren();
  }
  renderChangeCtl();
}

function scheduleDiffReload() {
  if (_diffDebounceTimer) clearTimeout(_diffDebounceTimer);
  _diffDebounceTimer = setTimeout(async () => {
    _diffDebounceTimer = null;
    if (!_change || _change.view !== 'diff') return;
    await loadDiff({ keepScroll: true });
    if (_change && _change.view === 'diff') {
      const d = _change.data;
      const noHunks = !d?.hunks || d.hunks.length === 0;
      if (noHunks && !d?.binary && !d?.too_large) {
        switchView('file');
      }
    }
  }, 300);
}

function updateEditorHeader() {
  const badge = document.getElementById('editor-badge');
  const btnSave = document.getElementById('btn-save');
  const btnReload = document.getElementById('btn-reload');
  if (!badge || !btnSave || !btnReload) return;
  const btnClose = document.getElementById('btn-close-file');
  if (btnClose) {
    btnClose.disabled = !_openPath;
    btnClose.hidden = _change?.view === 'diff';
  }

  if (!_openPath) {
    badge.textContent = '';
    badge.className = 'editor-badge';
    btnSave.disabled = true;
    btnReload.disabled = true;
    btnReload.classList.remove('highlight');
    return;
  }

  if (_diskChanged) {
    badge.textContent = _dirty ? '! conflict' : '↺ disk changed';
    badge.className = 'editor-badge conflict';
    btnSave.disabled = true;
    btnReload.disabled = false;
    btnReload.classList.add('highlight');
  } else if (_dirty) {
    badge.textContent = '● modified';
    badge.className = 'editor-badge modified';
    btnSave.disabled = false;
    btnReload.disabled = true;
    btnReload.classList.remove('highlight');
  } else {
    badge.textContent = '';
    badge.className = 'editor-badge';
    btnSave.disabled = true;
    btnReload.disabled = true;
    btnReload.classList.remove('highlight');
  }
}

// Refresh "Xs ago" every 15 seconds
setInterval(updateStatusBar, 15_000);

// Poll instances every 2s
setInterval(pollInstances, 2000);

// ── Phase 7: context menu + file operations ───────────────────────────

async function revealInExplorer(instId, relPath = '') {
  try {
    const r = await fetch(`${BASE}/reveal`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: instId, path: relPath }),
    });
    if (!r.ok) showNotification('error', 'Failed to reveal in Explorer');
  } catch (e) {
    showNotification('error', 'Failed to reveal in Explorer');
  }
}

let _ctxMenu = null;

function showContextMenu(e, instId, entry, after) {
  hideContextMenu();
  const menu = document.createElement('div');
  menu.className = 'ctx-menu';
  menu.style.left = e.clientX + 'px';
  menu.style.top = e.clientY + 'px';
  _ctxMenu = menu;

  const item = (label, cls, fn) => {
    const el = document.createElement('div');
    el.className = 'ctx-item' + (cls ? ' ' + cls : '');
    el.textContent = label;
    el.addEventListener('click', async () => { hideContextMenu(); await fn(); after?.(); });
    menu.appendChild(el);
    return el;
  };
  const sep = () => {
    const el = document.createElement('hr');
    el.className = 'ctx-sep';
    menu.appendChild(el);
  };

  if (entry.isRoot) {
    // Click derecho sobre zona vacía del explorador → acciones sobre la raíz.
    item('New file', '', () => promptNewFile(instId, ''));
    item('New folder', '', () => promptNewFolder(instId, ''));
    sep();
    item('Open terminal here', 'accent', () => openWindowsTerminal(instId));
    sep();
    item('Reveal in Explorer', '', () => revealInExplorer(instId, ''));
  } else {
    if (!entry.is_dir && isRunnable(entry.name)) {
      item('Ejecutar', 'accent', () => launchFile(instId, entry.path));
      sep();
    }
    if (!entry.is_dir && isHtml(entry.name)) {
      item('Open in browser', 'accent', () => launchFile(instId, entry.path));
      sep();
    }
    if (entry.is_dir) {
      item('New file', '', () => promptNewFile(instId, entry.path));
      item('New folder', '', () => promptNewFolder(instId, entry.path));
      sep();
    }
    item('Rename', '', () => promptRename(instId, entry));
    sep();
    item('Delete', 'danger', () => doDelete(instId, entry));
    sep();
    item('Reveal in Explorer', '', () => revealInExplorer(instId, entry.path));
  }

  document.body.appendChild(menu);
  const r = menu.getBoundingClientRect();
  const x = Math.max(4, Math.min(e.clientX, innerWidth - r.width - 4));
  const y = e.clientY + r.height + 4 > innerHeight
    ? Math.max(4, e.clientY - r.height)
    : e.clientY;
  menu.style.left = x + 'px';
  menu.style.top = y + 'px';
  document.addEventListener('mousedown', _ctxOutside, true);
  document.addEventListener('keydown', _ctxEscape, true);
}

function _ctxOutside(e) {
  if (_ctxMenu && !_ctxMenu.contains(e.target)) hideContextMenu();
}
function _ctxEscape(e) {
  if (e.key !== 'Escape') return;
  e.stopPropagation();
  hideContextMenu();
}
function hideContextMenu() {
  _ctxMenu?.remove();
  _ctxMenu = null;
  document.removeEventListener('mousedown', _ctxOutside, true);
  document.removeEventListener('keydown', _ctxEscape, true);
}

async function promptNewFile(instId, parentPath) {
  const name = prompt('New file name:');
  if (!name) return;
  const relPath = parentPath ? `${parentPath}/${name}` : name;
  try {
    const r = await fetch(`${BASE}/mkfile`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: instId, path: relPath }),
    });
    if (!r.ok) showNotification('error', `Failed to create file: ${r.statusText}`);
  } catch (e) { console.warn(e); }
}

async function promptNewFolder(instId, parentPath) {
  const name = prompt('New folder name:');
  if (!name) return;
  const relPath = parentPath ? `${parentPath}/${name}` : name;
  try {
    const r = await fetch(`${BASE}/mkdir`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: instId, path: relPath }),
    });
    if (!r.ok) showNotification('error', `Failed to create folder: ${r.statusText}`);
  } catch (e) { console.warn(e); }
}

async function promptRename(instId, entry) {
  let newName = prompt('New name:', entry.path.split('/').pop());
  if (!newName) return;
  if (instId === PROMPTS_ID && !/\.md$/i.test(newName)) newName += '.md';
  const parent = entry.path.includes('/') ? entry.path.split('/').slice(0, -1).join('/') : '';
  const dst = parent ? `${parent}/${newName}` : newName;
  try {
    const r = await fetch(`${BASE}/rename`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: instId, src: entry.path, dst }),
    });
    if (!r.ok) showNotification('error', `Failed to rename: ${r.statusText}`);
  } catch (e) { console.warn(e); }
}

async function doDelete(instId, entry) {
  const kind = entry.is_dir ? 'folder' : 'file';
  if (!confirm(`Delete ${kind} "${entry.path.split('/').pop()}"?`)) return;
  try {
    const r = await fetch(`${BASE}/file?id=${encodeURIComponent(instId)}&path=${encodeURIComponent(entry.path)}`,
      { method: 'DELETE' });
    if (!r.ok) showNotification('error', `Failed to delete: ${r.statusText}`);
  } catch (e) { console.warn(e); }
}

// ── Prompts ───────────────────────────────────────────────────────────
let _prompts = [];
let _palShown = [];
let _palSel = 0;
const palEl = () => document.getElementById('prompt-palette');

async function loadPrompts() {
  try {
    const r = await fetch(`${BASE}/prompts`);
    if (!r.ok) return false;
    _prompts = (await r.json()).prompts || [];
    return true;
  } catch {
    return false;
  }
}

async function openPalette() {
  if (!_activeId) return;
  if (!await loadPrompts()) {
    showNotification('error', 'Failed to load prompts.', 3000);
    return;
  }
  const input = document.getElementById('pal-input');
  input.value = '';
  document.getElementById('pal-target').textContent = `→ COM ${_termId}`;
  palEl().hidden = false;
  renderPalette(true);
  input.focus();
}

function closePalette({ refocus = true } = {}) {
  palEl().hidden = true;
  if (refocus) _term?.focus();
}

function renderPalette(resetSel = false) {
  const words = document.getElementById('pal-input').value.toLowerCase().split(/\s+/).filter(Boolean);
  _palShown = _prompts.filter(p => {
    const hay = p.path.replace(/\.md$/i, '').toLowerCase();
    return words.every(w => hay.includes(w));
  });
  if (resetSel) _palSel = 0;
  _palSel = Math.max(0, Math.min(_palSel, _palShown.length - 1));

  const noPrompts = _prompts.length === 0;
  document.getElementById('pal-body').hidden = noPrompts;
  document.getElementById('pal-empty').hidden = !noPrompts;

  const list = document.getElementById('pal-list');
  const prev = document.getElementById('pal-prev');
  list.replaceChildren();
  prev.textContent = '';
  if (noPrompts) return;
  if (_palShown.length === 0) {
    const none = document.createElement('div');
    none.className = 'pal-none';
    none.textContent = 'No matches';
    list.appendChild(none);
    return;
  }
  _palShown.forEach((p, i) => {
    const parts = p.path.split('/');
    const row = document.createElement('div');
    row.className = 'pal-row' + (i === _palSel ? ' on' : '');
    row.dataset.i = i;
    row.textContent = parts.pop().replace(/\.md$/i, '');
    const group = document.createElement('small');
    group.textContent = parts.join('/') || '\u00a0';
    row.appendChild(group);
    list.appendChild(row);
  });
  prev.textContent = _palShown[_palSel].content;
  list.querySelector('.pal-row.on')?.scrollIntoView({ block: 'nearest' });
}

function insertPrompt(p) {
  if (!p) return;
  const text = p.content.replace(/\s+$/, '');
  if (!text) {
    showNotification('warn', 'This prompt is empty.', 3000);
    return;
  }
  if (!_term || _termWs?.readyState !== WebSocket.OPEN) {
    showNotification('warn', 'Terminal not connected.', 3000);
    return;
  }
  if (text.includes('\n') && !_term.modes.bracketedPasteMode) {
    showNotification('warn', 'This terminal does not accept multi-line paste. Prompt not inserted.', 4000);
    return;
  }
  closePalette();
  _term.paste(text);
  _term.focus();
}

async function refreshPalette() {
  if (palEl().hidden || !await loadPrompts()) return;
  if (_openInstId === PROMPTS_ID && !_prompts.some(p => p.path === _openPath)) resetEditor();
  renderPalette();
  document.getElementById('pal-input').focus();
}

async function newPrompt() {
  let name = (prompt('New prompt name:') || '').trim().replace(/\\/g, '/');
  if (!name) {
    document.getElementById('pal-input').focus();
    return;
  }
  if (!/\.md$/i.test(name)) name += '.md';
  try {
    const r = await fetch(`${BASE}/mkfile`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: PROMPTS_ID, path: name }),
    });
    if (!r.ok) {
      showNotification('error', 'Failed to create prompt.', 3000);
      return;
    }
  } catch {
    showNotification('error', 'Failed to create prompt.', 3000);
    return;
  }
  closePalette({ refocus: false });
  openPrompt(name);
}

function editPrompt(p) {
  if (!p) return;
  closePalette({ refocus: false });
  openPrompt(p.path);
}

function initPalette() {
  const input = document.getElementById('pal-input');
  const list = document.getElementById('pal-list');

  document.getElementById('btn-prompts')?.addEventListener('click', openPalette);
  input.addEventListener('input', () => renderPalette(true));

  input.addEventListener('keydown', e => {
    const ctrl = e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      _palSel += e.key === 'ArrowDown' ? 1 : -1;
      renderPalette();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      insertPrompt(_palShown[_palSel]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      closePalette();
    } else if (ctrl && e.code === 'KeyN') {
      e.preventDefault();
      newPrompt();
    } else if (ctrl && e.code === 'KeyE') {
      e.preventDefault();
      editPrompt(_palShown[_palSel]);
    }
  });

  const rowIndex = e => {
    const row = e.target.closest('.pal-row');
    return row ? Number(row.dataset.i) : -1;
  };
  list.addEventListener('click', e => {
    const i = rowIndex(e);
    if (i < 0) return;
    _palSel = i;
    renderPalette();
  });
  list.addEventListener('dblclick', e => {
    const i = rowIndex(e);
    if (i >= 0) insertPrompt(_palShown[i]);
  });
  list.addEventListener('contextmenu', e => {
    e.preventDefault();
    const i = rowIndex(e);
    if (i < 0) return;
    _palSel = i;
    renderPalette();
    const p = _palShown[i];
    if (_dirty && _openInstId === PROMPTS_ID && _openPath === p.path) {
      showNotification('warn', 'Save or discard the open prompt first.', 3000);
      return;
    }
    showContextMenu(e, PROMPTS_ID, { name: p.path.split('/').pop(), path: p.path, is_dir: false }, refreshPalette);
  });

  document.getElementById('pal-new')?.addEventListener('click', newPrompt);
  document.getElementById('pal-folder')?.addEventListener('click', () => revealInExplorer(PROMPTS_ID, ''));

  palEl().addEventListener('mousedown', e => {
    if (e.target === palEl()) {
      closePalette();
      return;
    }
    if (e.target !== input) e.preventDefault();
  });
}

// ── Draggable dividers ────────────────────────────────────────────────
function initDividers() {
  const root = document.documentElement;
  const body = document.querySelector('.body');
  document.querySelectorAll('.divider').forEach(div => {
    // Bloqueo contundente de eventos nativos de arrastre del OS
    div.style.webkitAppRegion = 'no-drag';
    div.addEventListener('mousedown', e => e.stopPropagation(), true);

    div.addEventListener('pointerdown', e => {
      e.preventDefault();
      e.stopPropagation();

      // Capturar el puntero asegura que todos los eventos de ratón vayan al separador
      // incluso si el cursor se sale de la ventana.
      div.setPointerCapture(e.pointerId);
      div.classList.add('dragging');
      const target = div.dataset.target;
      const dragClass = target === 'sessions' ? 'is-dragging-row' : 'is-dragging';
      document.body.classList.add(dragClass);

      const onMove = ev => {
        const rect = body.getBoundingClientRect();
        if (target === 'sidebar') {
          const w = Math.max(180, Math.min(ev.clientX - rect.left, 480));
          root.style.setProperty('--col-sidebar', w + 'px');
        } else if (target === 'sessions') {
          const sb = document.querySelector('.sidebar').getBoundingClientRect();
          const head = document.querySelector('.sessions .pane-head');
          const list = document.getElementById('activitybar');
          const contentH = (head ? head.offsetHeight : 0) + (list ? list.scrollHeight : 0);
          let h = Math.max(80, Math.min(ev.clientY - sb.top, sb.height - 120));
          if (contentH > 0) {
            h = Math.min(h, contentH);
          }
          h = Math.max(80, h);
          root.style.setProperty('--row-sessions', h + 'px');
        } else {
          const w = Math.max(220, Math.min(rect.right - ev.clientX, rect.width * 0.6));
          root.style.setProperty('--col-terminal', w + 'px');
        }
      };

      const onUp = ev => {
        div.releasePointerCapture(ev.pointerId);
        div.classList.remove('dragging');
        document.body.classList.remove(dragClass);
        div.removeEventListener('pointermove', onMove);
        div.removeEventListener('pointerup', onUp);
        div.removeEventListener('pointercancel', onUp);
        _fit?.fit();
      };

      div.addEventListener('pointermove', onMove);
      div.addEventListener('pointerup', onUp);
      div.addEventListener('pointercancel', onUp);
    });

    div.addEventListener('dblclick', e => {
      e.stopPropagation();
      if (div.dataset.target === 'sidebar')
        root.style.setProperty('--col-sidebar', '240px');
      else if (div.dataset.target === 'sessions')
        root.style.setProperty('--row-sessions', '320px');
      else
        root.style.setProperty('--col-terminal', 'calc(33% + 68px)');
      _fit?.fit();
    });
  });
}

function initSessionListEvents() {
  const bar = document.getElementById('activitybar');
  if (!bar) return;
  bar.addEventListener('pointerenter', () => {
    _listHover = true;
  });
  bar.addEventListener('pointerleave', () => {
    _listHover = false;
    if (_layoutPending) {
      applyLayout();
    }
  });
  bar.addEventListener('scroll', updateListFade, { passive: true });
  if (typeof ResizeObserver !== 'undefined') {
    const ro = new ResizeObserver(() => {
      updateListFade();
    });
    ro.observe(bar);
  }
}

// ── Boot ──────────────────────────────────────────────────────────────
updateWrapButton();
initWindowControls();
initSessionListEvents();
document.getElementById('btn-add-session')?.addEventListener('click', addInstance);
document.getElementById('btn-changes')?.addEventListener('click', () => {
  if (_changesFilterIds.has(_activeId)) _changesFilterIds.delete(_activeId);
  else _changesFilterIds.add(_activeId);
  renderChangesFilter();
});
document.getElementById('git-count')?.addEventListener('click', () => {
  if (!_git.total) return;
  if (confirmDiscard()) { resetDiff(); resetEditor(); }
});
document.querySelectorAll('#change-ctl .seg-ctl button').forEach(btn => {
  btn.addEventListener('click', () => switchView(btn.dataset.view));
});
document.getElementById('change-prev')?.addEventListener('click', () => {
  if (_change && _change.view === 'diff') selectHunk(_change.idx - 1);
});
document.getElementById('change-next')?.addEventListener('click', () => {
  if (_change && _change.view === 'diff') selectHunk(_change.idx + 1);
});
document.getElementById('change-close')?.addEventListener('click', closeChange);
document.getElementById('btn-close-file')?.addEventListener('click', closeChange);

const diffViewEl = document.getElementById('diff-view');
if (diffViewEl) {
  diffViewEl.addEventListener('mouseenter', () => { _diffHovered = true; });
  diffViewEl.addEventListener('mouseleave', () => { _diffHovered = false; });
}

window.addEventListener('keydown', e => {
  const dv = document.getElementById('diff-view');
  if (!_change || _change.view !== 'diff' || !dv) return;
  const isFocusInDiff = document.activeElement && (dv === document.activeElement || dv.contains(document.activeElement));
  const ae = document.activeElement;
  if (!isFocusInDiff && ae?.closest?.('#terminal, #editor')) return; // Esc es del agente; Alt+↑/↓ de CodeMirror
  if (!_diffHovered && !isFocusInDiff) return;

  if (e.key === 'Escape') {
    e.preventDefault();
    closeChange();
  } else if (e.altKey && (e.key === 'ArrowUp' || e.key === 'Up')) {
    e.preventDefault();
    selectHunk(_change.idx - 1);
  } else if (e.altKey && (e.key === 'ArrowDown' || e.key === 'Down')) {
    e.preventDefault();
    selectHunk(_change.idx + 1);
  }
});
// xterm measures glyph cells once at init: make sure the mono font is ready first (local file, few ms)
await Promise.all([
  document.fonts.load('13px "JetBrains Mono"'),
  document.fonts.load('700 13px "JetBrains Mono"'),
]).catch(() => {});
initTerminal();
initPalette();
initEditor();
initDividers();
initEmptyAreaTerminal();
loadInstances();
