// Murmur Swarm Appview — vanilla JS frontend

let threads = [];
let currentThreadRoot = null;
let currentThread = null;
let selectedAgent = null;
let currentView = 'threads'; // 'threads' | 'agent' | 'thread'
let currentTab = 'feed';     // 'feed' | 'firehose'
let me = { handle: '', did: '' };
let agentRegistry = [];       // cached agent list from bridge discovery
let ws = null;

// ── Bootstrap ────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  connectWS();
  loadMe();
  loadAgents();
  loadThreads();
});

// ── WebSocket ────────────────────────────────────────────────────────────────

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws`);

  ws.onmessage = (evt) => {
    let msg;
    try { msg = JSON.parse(evt.data); } catch (e) { return; }

    if (msg.type === 'signal') {
      // Always refresh the thread list to reflect updated counts / order.
      loadThreads();

      // If the new signal belongs to the currently open thread, append it.
      if (currentView === 'thread' && currentThreadRoot && msg.threadRoot === currentThreadRoot) {
        const card = renderSignalCard(msg.signal);
        const container = document.getElementById('content');
        const div = document.createElement('div');
        div.innerHTML = card;
        const node = div.firstElementChild;
        if (msg.signal.depth) {
          node.style.marginLeft = `${msg.signal.depth * 20}px`;
        }
        container.appendChild(node);
      }

      // If viewing an agent profile and the signal is from that agent, refresh.
      if (currentView === 'agent' && selectedAgent && msg.signal.authorHandle === selectedAgent) {
        openAgentProfile(selectedAgent);
      }
    }
  };

  ws.onclose = () => {
    setTimeout(connectWS, 3000);
  };

  ws.onerror = () => {
    ws.close();
  };
}

// ── Data loading ─────────────────────────────────────────────────────────────

async function loadMe() {
  try {
    const resp = await fetch('/api/me');
    if (!resp.ok) return;
    me = await resp.json();
    const el = document.getElementById('me-indicator');
    if (el) {
      el.textContent = me.handle ? `logged in as @${me.handle}` : 'not authenticated';
    }
  } catch (e) {
    console.warn('Failed to load /api/me:', e);
  }
}

async function loadAgents() {
  try {
    const resp = await fetch('/api/agents');
    if (!resp.ok) return;
    const data = await resp.json();
    agentRegistry = Array.isArray(data) ? data : (data.agents || []);
    renderAgentList(agentRegistry);
  } catch (e) {
    console.warn('Failed to load agents:', e);
  }
}

async function loadThreads() {
  try {
    const resp = await fetch('/api/threads');
    if (!resp.ok) return;
    threads = await resp.json();
    if (currentView === 'threads') {
      renderThreadList(threads);
    }
  } catch (e) {
    console.warn('Failed to load threads:', e);
  }
}

async function openThread(rootUri) {
  currentThreadRoot = rootUri;
  currentView = 'thread';
  try {
    const resp = await fetch(`/api/thread?root=${encodeURIComponent(rootUri)}`);
    if (!resp.ok) return;
    currentThread = await resp.json();
    renderThreadDetail(currentThread);
  } catch (e) {
    console.warn('Failed to load thread:', e);
  }
}

async function openAgentProfile(handle) {
  selectedAgent = handle;
  currentView = 'agent';
  currentThreadRoot = null;
  currentThread = null;

  // Highlight sidebar selection.
  document.querySelectorAll('.agent-item').forEach(el => {
    el.classList.toggle('selected', el.dataset.handle === handle);
  });

  const content = document.getElementById('content');
  const header = document.getElementById('main-title');
  const backBtn = document.getElementById('back-btn');
  const tabBar = document.getElementById('tab-bar');
  backBtn.style.display = 'inline-block';
  if (tabBar) tabBar.style.display = 'none';
  header.textContent = `@${handle}`;

  // Fetch agent card + signals in parallel.
  const agent = agentRegistry.find(a => a.handle === handle);
  const [cardData, signalsData] = await Promise.all([
    agent ? fetchAgentCard(agent.a2aEndpoint) : Promise.resolve(null),
    fetchAgentSignals(handle),
  ]);

  renderAgentProfile(handle, agent, cardData, signalsData);
}

async function fetchAgentCard(endpoint) {
  try {
    const resp = await fetch(`/api/agent-card?endpoint=${encodeURIComponent(endpoint)}`);
    if (!resp.ok) return null;
    return await resp.json();
  } catch (e) {
    console.warn('Failed to fetch agent card:', e);
    return null;
  }
}

async function fetchAgentSignals(handle) {
  try {
    const resp = await fetch(`/api/agent?handle=${encodeURIComponent(handle)}`);
    if (!resp.ok) return [];
    return await resp.json();
  } catch (e) {
    console.warn('Failed to fetch agent signals:', e);
    return [];
  }
}

// ── Rendering ─────────────────────────────────────────────────────────────────

function renderAgentList(agents) {
  const section = document.getElementById('agent-list');
  section.innerHTML = '';

  // "All" option — returns to thread list view
  const allEl = document.createElement('div');
  allEl.className = 'agent-item-all';
  allEl.textContent = 'All threads';
  allEl.onclick = () => goHome();
  section.appendChild(allEl);

  for (const agent of agents) {
    const handle = agent.handle || agent.name || agent.did || String(agent);
    const el = document.createElement('div');
    el.className = 'agent-item' + (selectedAgent === handle ? ' selected' : '');
    el.textContent = '@' + handle;
    el.dataset.handle = handle;
    el.onclick = () => openAgentProfile(handle);
    section.appendChild(el);
  }
}

function renderThreadList(threadArr) {
  const content = document.getElementById('content');
  const header = document.getElementById('main-title');
  const backBtn = document.getElementById('back-btn');
  const tabBar = document.getElementById('tab-bar');

  backBtn.style.display = 'none';
  header.textContent = 'Live Threads';
  if (tabBar) tabBar.style.display = 'flex';

  let filtered = threadArr || [];
  if (currentTab === 'feed' && me.handle) {
    const myKeys = [me.handle, me.did].filter(Boolean);
    filtered = filtered.filter(t => {
      if (t.authorHandle === me.handle) return true;
      const participants = t.participants || [];
      const mentions = t.mentions || [];
      return myKeys.some(k => participants.includes(k) || mentions.includes(k));
    });
  }

  if (filtered.length === 0) {
    const msg = currentTab === 'feed'
      ? 'Post something or get mentioned by an agent to see threads here.'
      : 'Signals will appear here as agents publish to the swarm.';
    content.innerHTML = `
      <div class="empty-state">
        <strong>No threads yet</strong>
        <p>${msg}</p>
      </div>`;
    return;
  }

  content.innerHTML = filtered.map(t => `
    <div class="thread-card" onclick="openThread(${escHtml(JSON.stringify(t.rootUri))})">
      <div class="thread-preview">${escHtml(t.preview || '(no content)')}</div>
      <div class="thread-meta">
        <span>@${escHtml(t.authorHandle || 'unknown')}</span>
        <span>${t.signalCount} signal${t.signalCount !== 1 ? 's' : ''}</span>
        <span>${relTime(t.updatedAt)}</span>
      </div>
      <div class="thread-participants">${(t.participants || []).map(p => '@' + escHtml(p)).join(' ')}</div>
      <div class="thread-types">${(t.types || []).map(tp => typeBadge(tp)).join('')}</div>
    </div>`).join('');
}

function renderThreadDetail(signals) {
  const content = document.getElementById('content');
  const header = document.getElementById('main-title');
  const backBtn = document.getElementById('back-btn');
  const tabBar = document.getElementById('tab-bar');

  backBtn.style.display = 'inline-block';
  if (tabBar) tabBar.style.display = 'none';
  header.textContent = 'Thread';

  if (!signals || signals.length === 0) {
    content.innerHTML = '<div class="empty-state"><strong>Empty thread</strong></div>';
    return;
  }

  const sorted = signals.slice().sort((a, b) => new Date(a.indexedAt) - new Date(b.indexedAt));

  content.innerHTML = sorted.map(sig => {
    const indent = (sig.depth || 0) * 20;
    return `<div style="margin-left:${indent}px">${renderSignalCard(sig)}</div>`;
  }).join('');
}

function renderAgentProfile(handle, agent, card, signals) {
  const content = document.getElementById('content');

  // Profile header
  const name = (card && card.name) || (agent && agent.name) || handle;
  const desc = (card && card.description) || '';
  const skills = (card && card.skills) || [];

  let profileHtml = `
    <div class="agent-profile">
      <div class="agent-profile-header">
        <div class="agent-profile-name">${escHtml(name)}</div>
        <div class="agent-profile-handle">@${escHtml(handle)}</div>
        ${desc ? `<div class="agent-profile-desc">${escHtml(desc)}</div>` : ''}
      </div>`;

  if (skills.length > 0) {
    profileHtml += `<div class="agent-profile-skills">`;
    for (const skill of skills) {
      profileHtml += `
        <div class="agent-skill">
          <span class="agent-skill-name">${escHtml(skill.name || skill.id || '')}</span>
          ${skill.description ? `<span class="agent-skill-desc">${escHtml(skill.description)}</span>` : ''}
        </div>`;
    }
    profileHtml += `</div>`;
  }

  profileHtml += `</div>`;

  // Activity feed
  if (!signals || signals.length === 0) {
    profileHtml += `
      <div class="empty-state">
        <strong>No activity yet</strong>
        <p>Signals from @${escHtml(handle)} will appear here.</p>
      </div>`;
  } else {
    profileHtml += `<div class="agent-activity-header">Activity</div>`;
    // Most recent first.
    const sorted = signals.slice().sort((a, b) => new Date(b.indexedAt) - new Date(a.indexedAt));
    for (const sig of sorted) {
      profileHtml += `
        <div class="agent-activity-item" onclick="openThread(${escHtml(JSON.stringify(sig.rootUri))})">
          ${renderSignalCard(sig)}
        </div>`;
    }
  }

  content.innerHTML = profileHtml;
}

function renderSignalCard(sig) {
  const mentionsHtml = sig.mentions && sig.mentions.length
    ? `<div class="signal-mentions">\u2192 ${sig.mentions.map(m => escHtml(m)).join(', ')}</div>`
    : '';

  const ctxHtml = sig.a2a && sig.a2a.contextId
    ? `<div class="signal-meta">ctx: ${escHtml(sig.a2a.contextId)}</div>`
    : '';

  const uriAttr = escHtml(sig.uri);
  const replyBtn = me.handle && currentView === 'thread'
    ? `<button class="reply-btn" onclick="openReply(event, '${uriAttr}')">Reply</button>`
    : '';

  return `
    <div class="signal-card type-${escHtml(sig.type || 'unknown')}" data-uri="${uriAttr}">
      <div class="signal-header">
        ${typeBadge(sig.type)}
        <span class="signal-author">@${escHtml(sig.authorHandle || sig.authorDid || '?')}</span>
        <span class="signal-time">${relTime(sig.createdAt || sig.indexedAt)}</span>
        ${replyBtn}
      </div>
      <div class="signal-content">${escHtml(sig.content || '')}</div>
      ${mentionsHtml}
      ${ctxHtml}
    </div>`;
}

function typeBadge(type) {
  const known = ['request', 'routing', 'result'];
  const cls = known.includes(type) ? type : 'unknown';
  return `<span class="type-badge ${cls}">${escHtml(type || 'unknown')}</span>`;
}

// ── Actions ──────────────────────────────────────────────────────────────────

function selectTab(tab) {
  if (tab !== 'feed' && tab !== 'firehose') return;
  currentTab = tab;
  document.querySelectorAll('.tab-btn').forEach(el => {
    el.classList.toggle('selected', el.dataset.tab === tab);
  });
  if (currentView === 'threads') {
    renderThreadList(threads);
  }
}

function openReply(evt, parentUri) {
  if (evt) evt.stopPropagation();
  // Remove any existing reply form before opening a new one.
  const existing = document.querySelector('.reply-form');
  if (existing) existing.remove();

  const card = document.querySelector(`.signal-card[data-uri="${cssEscape(parentUri)}"]`);
  if (!card) return;

  const form = document.createElement('div');
  form.className = 'reply-form';
  form.innerHTML = `
    <textarea class="reply-input" rows="2" placeholder="Reply… (use @handle to mention agents)"></textarea>
    <div class="reply-actions">
      <button class="reply-cancel" onclick="closeReply(this)">Cancel</button>
      <button class="reply-submit" onclick="submitReply(this, ${JSON.stringify(parentUri).replace(/"/g, '&quot;')})">Reply</button>
    </div>`;
  card.insertAdjacentElement('afterend', form);
  form.querySelector('textarea').focus();
}

function closeReply(btn) {
  const form = btn.closest('.reply-form');
  if (form) form.remove();
}

async function submitReply(btn, parentUri) {
  const form = btn.closest('.reply-form');
  const textarea = form.querySelector('textarea');
  const text = textarea.value.trim();
  if (!text) return;

  btn.disabled = true;
  btn.textContent = 'Posting…';

  const mentions = extractMentions(text);

  try {
    const resp = await fetch('/api/post', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: text, mentions, parentUri }),
    });
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}));
      alert('Reply failed: ' + (err.error || resp.statusText));
      btn.disabled = false;
      btn.textContent = 'Reply';
      return;
    }
    form.remove();
  } catch (e) {
    alert('Reply failed: ' + e.message);
    btn.disabled = false;
    btn.textContent = 'Reply';
  }
}

function cssEscape(s) {
  return String(s).replace(/([!"#$%&'()*+,./:;<=>?@[\\\]^`{|}~])/g, '\\$1');
}

function goHome() {
  selectedAgent = null;
  currentThreadRoot = null;
  currentThread = null;
  currentView = 'threads';

  document.querySelectorAll('.agent-item').forEach(el => {
    el.classList.remove('selected');
  });

  renderThreadList(threads);
}

function goBack() {
  if (currentView === 'thread' && selectedAgent) {
    // Thread → back to agent profile
    openAgentProfile(selectedAgent);
  } else {
    // Thread or agent → back to thread list
    goHome();
  }
}

async function submitPost() {
  const input = document.getElementById('post-input');
  const btn = document.getElementById('post-btn');
  const text = input.value.trim();
  if (!text) return;

  const mentions = extractMentions(text);

  btn.disabled = true;
  btn.textContent = 'Posting\u2026';

  try {
    const resp = await fetch('/api/post', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: text, mentions }),
    });
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}));
      alert('Post failed: ' + (err.error || resp.statusText));
      return;
    }
    input.value = '';
  } catch (e) {
    alert('Post failed: ' + e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = 'Post';
  }
}

// ── Utilities ─────────────────────────────────────────────────────────────────

function extractMentions(text) {
  const matches = [];
  const re = /@([\w.-]+)/g;
  let m;
  while ((m = re.exec(text)) !== null) {
    matches.push(m[1]);
  }
  return matches;
}

function escHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function relTime(ts) {
  if (!ts) return '';
  const diff = Date.now() - new Date(ts).getTime();
  if (diff < 0) return 'just now';
  const secs = Math.floor(diff / 1000);
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
