// gitscope UI: every view reads the same filter state, which lives in the URL.

const $ = (sel) => document.querySelector(sel);
const num = new Intl.NumberFormat('pt-BR');
const DAY = 86400;
const PAGE = 100;

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const date = (unix) => unix ? new Date(unix * 1000).toLocaleDateString('pt-BR') : '—';
const dateTime = (unix) => new Date(unix * 1000).toLocaleString('pt-BR', { dateStyle: 'medium', timeStyle: 'short' });
const toInputDate = (unix) => {
  const d = new Date(unix * 1000);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
const fromInputDate = (s) => {
  const [y, m, d] = s.split('-').map(Number);
  return Math.floor(new Date(y, m - 1, d).getTime() / 1000);
};
const startOfToday = () => { const d = new Date(); d.setHours(0, 0, 0, 0); return d.getTime() / 1000; };

const state = { ref: '', authors: [], since: 0, until: 0, path: '', q: '', merges: false, tab: 'commits' };
let repo = null;
let allAuthors = [];
let generation = 0;
let commitsOffset = 0;

// ---------- URL and API ----------

function readURL() {
  const p = new URLSearchParams(location.search);
  state.ref = p.get('ref') || '';
  state.authors = p.getAll('author');
  state.since = Number(p.get('since')) || 0;
  state.until = Number(p.get('until')) || 0;
  state.path = p.get('path') || '';
  state.q = p.get('q') || '';
  state.merges = p.get('merges') === '1';
  state.tab = p.get('tab') || 'commits';
}

function filterParams() {
  const p = new URLSearchParams();
  if (state.ref) p.set('ref', state.ref);
  state.authors.forEach((a) => p.append('author', a));
  if (state.since) p.set('since', state.since);
  if (state.until) p.set('until', state.until);
  if (state.path) p.set('path', state.path);
  if (state.q) p.set('q', state.q);
  if (state.merges) p.set('merges', '1');
  return p;
}

function writeURL() {
  const p = filterParams();
  if (state.tab !== 'commits') p.set('tab', state.tab);
  const qs = p.toString();
  history.replaceState(null, '', qs ? `?${qs}` : location.pathname);
}

async function api(path, params = filterParams(), init) {
  const res = await fetch(`/api/${path}?${params}`, init);
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}

// ---------- refresh cycle ----------

function update(changes) {
  Object.assign(state, changes);
  refresh();
}

async function refresh() {
  const g = ++generation;
  writeURL();
  syncInputs();
  renderChips();
  $('#error').hidden = true;
  try {
    const bucket = pickBucket();
    const [summary, timeline] = await Promise.all([
      api('summary'),
      api('timeline', withParam(filterParams(), 'bucket', bucket)),
    ]);
    if (g !== generation) return;
    renderCards(summary);
    renderTimeline(timeline);
    await renderTab(g);
  } catch (err) {
    if (g === generation) showError(err);
  }
}

function withParam(p, key, value) { p.set(key, value); return p; }

function showError(err) {
  $('#error').textContent = `Erro: ${err.message}`;
  $('#error').hidden = false;
}

// Day buckets for short ranges, weeks for months, months for years.
function pickBucket() {
  const from = state.since || repo?.first || 0;
  const to = state.until || (repo?.last ? repo.last + DAY : 0);
  const span = (to - from) / DAY;
  if (span > 730) return 'month';
  if (span > 120) return 'week';
  return 'day';
}

// ---------- filters ----------

function syncInputs() {
  $('#f-ref').value = state.ref;
  $('#f-since').value = state.since ? toInputDate(state.since) : '';
  $('#f-until').value = state.until ? toInputDate(state.until - DAY) : '';
  if (document.activeElement !== $('#f-path')) $('#f-path').value = state.path;
  if (document.activeElement !== $('#f-q')) $('#f-q').value = state.q;
  $('#f-merges').checked = state.merges;
  $('#f-authors-btn').textContent = state.authors.length
    ? `${state.authors.length} selecionado(s)` : 'Todos';
  document.querySelectorAll('#tabs button').forEach((b) => b.classList.toggle('on', b.dataset.tab === state.tab));
  const today = startOfToday();
  document.querySelectorAll('#f-presets button').forEach((b) => {
    const days = Number(b.dataset.days);
    const on = days === 0 ? !state.since && !state.until : !state.until && state.since === today - days * DAY;
    b.classList.toggle('on', on);
  });
}

function authorName(email) {
  return allAuthors.find((a) => a.email === email)?.name || email;
}

function renderChips() {
  const chips = [];
  if (state.ref) chips.push([`branch: ${state.ref}`, { ref: '' }]);
  state.authors.forEach((a) =>
    chips.push([`autor: ${authorName(a)}`, { authors: state.authors.filter((x) => x !== a) }]));
  if (state.since || state.until) {
    const from = state.since ? date(state.since) : 'início';
    const to = state.until ? date(state.until - DAY) : 'hoje';
    chips.push([`período: ${from} – ${to}`, { since: 0, until: 0 }]);
  }
  if (state.path) chips.push([`caminho: ${state.path}`, { path: '' }]);
  if (state.q) chips.push([`mensagem: "${state.q}"`, { q: '' }]);
  if (state.merges) chips.push(['com merges', { merges: false }]);

  const box = $('#chips');
  box.replaceChildren(...chips.map(([label, changes]) => {
    const el = document.createElement('span');
    el.className = 'chip';
    el.innerHTML = `${esc(label)}<button type="button" aria-label="Remover filtro">×</button>`;
    el.querySelector('button').onclick = () => update(changes);
    return el;
  }));
  if (chips.length > 1) {
    const clear = document.createElement('button');
    clear.className = 'link';
    clear.textContent = 'Limpar tudo';
    clear.onclick = () => update({ ref: '', authors: [], since: 0, until: 0, path: '', q: '', merges: false });
    box.append(clear);
  }
}

async function loadRefs() {
  const refs = await api('refs', new URLSearchParams());
  const group = (label, list) => list.length
    ? `<optgroup label="${label}">${list.map((r) => `<option value="${esc(r.name)}">${esc(r.name)}</option>`).join('')}</optgroup>`
    : '';
  $('#f-ref').innerHTML = '<option value="">Todas as branches</option>' +
    group('Locais', refs.filter((r) => !r.remote)) + group('Remotas', refs.filter((r) => r.remote));
  $('#f-ref').value = state.ref;
}

function renderAuthorPicker() {
  const term = $('#f-authors-search').value.trim().toLowerCase();
  const list = allAuthors.filter((a) => !term || a.name.toLowerCase().includes(term) || a.email.includes(term));
  $('#f-authors-list').innerHTML = list.map((a) => `
    <label class="picker-item">
      <input type="checkbox" value="${esc(a.email)}" ${state.authors.includes(a.email) ? 'checked' : ''}>
      <span class="name" title="${esc(a.email)}">${esc(a.name)}</span>
      <span class="muted num">${num.format(a.commits)}</span>
    </label>`).join('') || '<div class="empty">Nenhum autor</div>';
}

function bindFilters() {
  $('#f-ref').onchange = (e) => update({ ref: e.target.value });
  $('#f-merges').onchange = (e) => update({ merges: e.target.checked });
  $('#f-since').onchange = (e) => update({ since: e.target.value ? fromInputDate(e.target.value) : 0 });
  $('#f-until').onchange = (e) => update({ until: e.target.value ? fromInputDate(e.target.value) + DAY : 0 });
  document.querySelectorAll('#f-presets button').forEach((b) => {
    b.onclick = () => {
      const days = Number(b.dataset.days);
      update({ since: days ? startOfToday() - days * DAY : 0, until: 0 });
    };
  });

  let timer;
  const debounced = (key) => (e) => {
    clearTimeout(timer);
    timer = setTimeout(() => update({ [key]: e.target.value.trim() }), 300);
  };
  $('#f-path').oninput = debounced('path');
  $('#f-q').oninput = debounced('q');

  const pop = $('#f-authors-pop');
  $('#f-authors-btn').onclick = () => {
    pop.hidden = !pop.hidden;
    if (!pop.hidden) { renderAuthorPicker(); $('#f-authors-search').focus(); }
  };
  $('#f-authors-search').oninput = renderAuthorPicker;
  $('#f-authors-list').onchange = (e) => {
    const email = e.target.value;
    update({ authors: e.target.checked ? [...state.authors, email] : state.authors.filter((a) => a !== email) });
  };
  $('#f-authors-clear').onclick = () => { update({ authors: [] }); renderAuthorPicker(); };
  document.addEventListener('click', (e) => {
    if (!pop.hidden && !e.target.closest('.picker')) pop.hidden = true;
  });

  document.querySelectorAll('#tabs button').forEach((b) => {
    b.onclick = () => { if (state.tab !== b.dataset.tab) update({ tab: b.dataset.tab }); };
  });

  $('#sync-btn').onclick = async () => {
    const btn = $('#sync-btn');
    btn.disabled = true;
    btn.textContent = 'Atualizando…';
    try {
      const res = await api('sync', new URLSearchParams(), { method: 'POST' });
      await loadRepo();
      await loadRefs();
      allAuthors = await api('authors', new URLSearchParams({ merges: '1' }));
      btn.textContent = res.added || res.removed ? `+${res.added} / −${res.removed} commits` : 'Já estava em dia';
      refresh();
    } catch (err) {
      showError(err);
      btn.textContent = 'Atualizar índice';
    } finally {
      btn.disabled = false;
      setTimeout(() => { btn.textContent = 'Atualizar índice'; }, 2500);
    }
  };
}

// ---------- header and cards ----------

async function loadRepo() {
  repo = await api('repo', new URLSearchParams());
  document.title = `${repo.name} · gitscope`;
  $('#repo-name').textContent = repo.name;
  const parts = [`${num.format(repo.commits)} commits`];
  if (repo.first) parts.push(`${date(repo.first)} – ${date(repo.last)}`);
  if (repo.head) parts.push(`HEAD em ${repo.head}`);
  $('#repo-meta').textContent = parts.join(' · ');
}

function renderCards(s) {
  const cards = [
    ['Commits', num.format(s.commits), s.first ? `${date(s.first)} – ${date(s.last)}` : 'nenhum no filtro'],
    ['Autores', num.format(s.authors), ''],
    ['Linhas adicionadas', `<span class="add">+${num.format(s.insertions)}</span>`, ''],
    ['Linhas removidas', `<span class="del">−${num.format(s.deletions)}</span>`, ''],
    ['Arquivos tocados', num.format(s.files), ''],
  ];
  $('#cards').innerHTML = cards.map(([label, value, sub]) => `
    <div class="card"><div class="label">${label}</div><div class="value">${value}</div>
    <div class="sub">${esc(sub) || '&nbsp;'}</div></div>`).join('');
}

// ---------- timeline ----------

const bucketNames = { day: 'por dia', week: 'por semana', month: 'por mês' };

function bucketEnd(start, bucket) {
  const [y, m, d] = start.split('-').map(Number);
  const t = bucket === 'month' ? new Date(y, m, 1) : new Date(y, m - 1, d + (bucket === 'week' ? 7 : 1));
  return t.getTime() / 1000;
}

function bucketLabel(start, bucket) {
  const [y, m, d] = start.split('-').map(Number);
  const t = new Date(y, m - 1, d);
  if (bucket === 'month') return t.toLocaleDateString('pt-BR', { month: 'short', year: '2-digit' });
  return t.toLocaleDateString('pt-BR', { day: '2-digit', month: 'short' });
}

function renderTimeline({ bucket, buckets }) {
  $('#bucket-label').textContent = bucketNames[bucket] || '';
  const box = $('#timeline');
  const tip = $('#tooltip');
  if (!buckets.length) {
    box.innerHTML = '<div class="empty">Nenhum commit no filtro atual</div>';
    return;
  }

  const W = box.clientWidth || 800;
  const H = box.clientHeight || 190;
  const pad = { top: 8, right: 4, bottom: 22, left: 34 };
  const iw = W - pad.left - pad.right;
  const ih = H - pad.top - pad.bottom;
  const max = Math.max(1, ...buckets.map((b) => b.commits));
  const bw = iw / buckets.length;
  const gap = bw > 6 ? Math.min(3, bw * 0.2) : 0;
  const y = (v) => pad.top + ih - (v / max) * ih;

  const ticks = [0, Math.round(max / 2), max].filter((v, i, a) => a.indexOf(v) === i);
  const labelEvery = Math.max(1, Math.ceil(buckets.length / Math.max(1, Math.floor(iw / 70))));

  let svg = `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">`;
  for (const t of ticks) {
    svg += `<line class="grid" x1="${pad.left}" x2="${W - pad.right}" y1="${y(t)}" y2="${y(t)}"/>`;
    svg += `<text class="axis" x="${pad.left - 6}" y="${y(t) + 4}" text-anchor="end">${t}</text>`;
  }
  buckets.forEach((b, i) => {
    const x = pad.left + i * bw;
    const h = pad.top + ih - y(b.commits);
    if (b.commits) {
      svg += `<rect class="bar" data-i="${i}" x="${x + gap / 2}" y="${y(b.commits)}" width="${Math.max(1, bw - gap)}" height="${Math.max(1, h)}" rx="${bw > 8 ? 2 : 0}"/>`;
    }
    if (i % labelEvery === 0) {
      svg += `<text class="axis" x="${x + bw / 2}" y="${H - 6}" text-anchor="middle">${esc(bucketLabel(b.start, bucket))}</text>`;
    }
  });
  svg += `<rect class="brush" id="brush" y="${pad.top}" height="${ih}" width="0"/></svg>`;
  box.innerHTML = svg;

  const svgEl = box.querySelector('svg');
  const brush = box.querySelector('#brush');
  const indexAt = (e) => {
    const r = svgEl.getBoundingClientRect();
    const x = ((e.clientX - r.left) / r.width) * W - pad.left;
    return Math.max(0, Math.min(buckets.length - 1, Math.floor(x / bw)));
  };
  let dragFrom = null;
  let hovered = null;

  svgEl.onpointermove = (e) => {
    const i = indexAt(e);
    if (hovered !== i) {
      box.querySelector('.bar.hover')?.classList.remove('hover');
      box.querySelector(`.bar[data-i="${i}"]`)?.classList.add('hover');
      hovered = i;
    }
    const b = buckets[i];
    tip.innerHTML = `<strong>${esc(bucketLabel(b.start, bucket))}</strong> · ${num.format(b.commits)} commit(s)<br>` +
      `+${num.format(b.insertions)} −${num.format(b.deletions)} · ${b.authors} autor(es)`;
    const panel = box.parentElement.getBoundingClientRect();
    tip.hidden = false;
    const left = Math.min(e.clientX - panel.left + 12, panel.width - tip.offsetWidth - 8);
    tip.style.left = `${left}px`;
    tip.style.top = `${e.clientY - panel.top + 14}px`;
    if (dragFrom !== null) {
      const a = Math.min(dragFrom, i);
      const z = Math.max(dragFrom, i);
      brush.setAttribute('x', pad.left + a * bw);
      brush.setAttribute('width', (z - a + 1) * bw);
    }
  };
  svgEl.onpointerleave = () => {
    tip.hidden = true;
    box.querySelector('.bar.hover')?.classList.remove('hover');
    hovered = null;
  };
  svgEl.onpointerdown = (e) => {
    dragFrom = indexAt(e);
    svgEl.setPointerCapture(e.pointerId);
    brush.setAttribute('x', pad.left + dragFrom * bw);
    brush.setAttribute('width', bw);
  };
  svgEl.onpointerup = (e) => {
    if (dragFrom === null) return;
    const i = indexAt(e);
    const a = Math.min(dragFrom, i);
    const z = Math.max(dragFrom, i);
    dragFrom = null;
    const [y0, m0, d0] = buckets[a].start.split('-').map(Number);
    update({ since: new Date(y0, m0 - 1, d0).getTime() / 1000, until: bucketEnd(buckets[z].start, bucket) });
  };
  svgEl.ondblclick = () => update({ since: 0, until: 0 });
}

// ---------- tabs ----------

async function renderTab(g) {
  commitsOffset = 0;
  const body = $('#tab-body');
  if (state.tab === 'authors') {
    const rows = await api('authors');
    if (g === generation) body.innerHTML = authorsTable(rows);
  } else if (state.tab === 'files') {
    const rows = await api('files', withParam(filterParams(), 'limit', 200));
    if (g === generation) body.innerHTML = filesTable(rows);
  } else {
    const rows = await api('commits', withParam(filterParams(), 'limit', PAGE));
    if (g === generation) body.innerHTML = commitsTable(rows);
  }
}

function meter(value, max) {
  const pct = max ? (value / max) * 100 : 0;
  return `<div class="meter"><div class="track"><div class="fill" style="width:${pct.toFixed(1)}%"></div></div>` +
    `<span class="num">${num.format(value)}</span></div>`;
}

function commitRows(rows) {
  return rows.map((c) => `
    <tr class="row" data-hash="${esc(c.hash)}">
      <td class="mono">${esc(c.hash.slice(0, 7))}</td>
      <td class="subject" title="${esc(c.subject)}">${esc(c.subject)}${c.merge ? '<span class="tag">merge</span>' : ''}</td>
      <td>${esc(c.authorName)}</td>
      <td class="num muted">${date(c.authorTime)}</td>
      <td class="num"><span class="add">+${num.format(c.insertions)}</span> <span class="del">−${num.format(c.deletions)}</span></td>
    </tr>`).join('');
}

function commitsTable(rows) {
  if (!rows.length) return '<div class="empty">Nenhum commit no filtro atual</div>';
  return `<table><thead><tr><th>Hash</th><th>Mensagem</th><th>Autor</th><th class="num">Data</th><th class="num">Linhas</th></tr></thead>
    <tbody id="commit-rows">${commitRows(rows)}</tbody></table>
    ${rows.length === PAGE ? '<div class="more"><button class="btn" id="more-btn" type="button">Carregar mais</button></div>' : ''}`;
}

async function loadMoreCommits() {
  const g = generation;
  commitsOffset += PAGE;
  const p = filterParams();
  p.set('limit', PAGE);
  p.set('offset', commitsOffset);
  const rows = await api('commits', p);
  if (g !== generation) return;
  $('#commit-rows').insertAdjacentHTML('beforeend', commitRows(rows));
  if (rows.length < PAGE) $('#more-btn')?.parentElement.remove();
}

function authorsTable(rows) {
  if (!rows.length) return '<div class="empty">Nenhum autor no filtro atual</div>';
  const max = Math.max(...rows.map((r) => r.commits));
  return `<table><thead><tr><th>Autor</th><th>Commits</th><th class="num">Linhas</th><th class="num">Arquivos</th>
    <th class="num">Primeiro</th><th class="num">Último</th></tr></thead><tbody>
    ${rows.map((a) => `
      <tr class="row ${state.authors.includes(a.email) ? 'selected' : ''}" data-author="${esc(a.email)}" title="Clique para filtrar por este autor">
        <td><strong>${esc(a.name)}</strong><div class="muted mono">${esc(a.email)}</div></td>
        <td>${meter(a.commits, max)}</td>
        <td class="num"><span class="add">+${num.format(a.insertions)}</span> <span class="del">−${num.format(a.deletions)}</span></td>
        <td class="num">${num.format(a.files)}</td>
        <td class="num muted">${date(a.first)}</td>
        <td class="num muted">${date(a.last)}</td>
      </tr>`).join('')}
    </tbody></table>`;
}

function filesTable(rows) {
  if (!rows.length) return '<div class="empty">Nenhum arquivo no filtro atual</div>';
  const max = Math.max(...rows.map((r) => r.changes));
  return `<table><thead><tr><th>Arquivo</th><th>Commits que alteraram</th><th class="num">Churn</th>
    <th class="num">Autores</th><th class="num">Última alteração</th></tr></thead><tbody>
    ${rows.map((f) => `
      <tr class="row ${f.exists ? '' : 'gone'}" data-path="${esc(f.path)}" title="Clique para filtrar por este arquivo">
        <td class="path">${esc(f.path)}${f.exists ? '' : '<span class="tag">removido</span>'}</td>
        <td>${meter(f.changes, max)}</td>
        <td class="num">${num.format(f.churn)}</td>
        <td class="num">${num.format(f.authors)}</td>
        <td class="num muted">${date(f.last)}</td>
      </tr>`).join('')}
    </tbody></table>`;
}

function bindTabs() {
  $('#tab-body').addEventListener('click', (e) => {
    if (e.target.id === 'more-btn') { loadMoreCommits().catch(showError); return; }
    const row = e.target.closest('tr.row');
    if (!row) return;
    if (row.dataset.hash) openCommit(row.dataset.hash);
    else if (row.dataset.author) {
      const email = row.dataset.author;
      update({ authors: state.authors.includes(email) ? state.authors.filter((a) => a !== email) : [...state.authors, email] });
    } else if (row.dataset.path) update({ path: row.dataset.path, tab: 'commits' });
  });
}

// ---------- commit drawer ----------

function diffHTML(patch) {
  return patch.split('\n').map((line) => {
    let cls = '';
    if (line.startsWith('diff --git')) cls = 'l-file';
    else if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('index ')) cls = 'muted';
    else if (line.startsWith('@@')) cls = 'l-hunk';
    else if (line.startsWith('+')) cls = 'l-add';
    else if (line.startsWith('-')) cls = 'l-del';
    return `<span class="${cls}">${esc(line) || ' '}</span>`;
  }).join('');
}

async function openCommit(hash) {
  const body = $('#drawer-body');
  body.innerHTML = '<div class="empty">Carregando…</div>';
  $('#drawer').hidden = false;
  $('#backdrop').hidden = false;
  try {
    const { commit: c, patch, truncated } = await api(`commits/${encodeURIComponent(hash)}`, new URLSearchParams());
    body.innerHTML = `
      <h3>${esc(c.subject)}</h3>
      <div class="meta">
        <span class="mono">${esc(c.hash)}</span>
        <span>${esc(c.authorName)} &lt;${esc(c.authorEmail)}&gt;</span>
        <span>${dateTime(c.authorTime)}</span>
        ${c.parents.length ? `<span>pais: ${c.parents.map((p) => `<span class="mono">${esc(p.slice(0, 7))}</span>`).join(', ')}</span>` : '<span>commit inicial</span>'}
      </div>
      <table class="files"><tbody>
        ${c.changes.map((f) => `<tr><td class="path">${esc(f.path)}</td>
          <td class="num">${f.binary ? '<span class="muted">binário</span>'
            : `<span class="add">+${num.format(f.insertions)}</span> <span class="del">−${num.format(f.deletions)}</span>`}</td></tr>`).join('')}
      </tbody></table>
      ${patch ? `<pre class="diff">${diffHTML(patch)}</pre>` : '<div class="empty">Sem diff (merge ou commit vazio)</div>'}
      ${truncated ? '<p class="muted">Diff muito grande: exibindo só o início.</p>' : ''}`;
  } catch (err) {
    body.innerHTML = `<div class="error">${esc(err.message)}</div>`;
  }
}

function closeDrawer() {
  $('#drawer').hidden = true;
  $('#backdrop').hidden = true;
}

// ---------- boot ----------

async function main() {
  readURL();
  bindFilters();
  bindTabs();
  $('#drawer-close').onclick = closeDrawer;
  $('#backdrop').onclick = closeDrawer;
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { closeDrawer(); $('#f-authors-pop').hidden = true; }
  });
  let resizeTimer;
  window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(refresh, 200); });

  try {
    await loadRepo();
    await loadRefs();
    allAuthors = await api('authors', new URLSearchParams({ merges: '1' }));
  } catch (err) {
    showError(err);
  }
  refresh();
}

main();
