/* molt site — interactive JS */

/* ── Demo tabs ─────────────────────────────────────────────────────────── */
function initDemoTabs() {
  const tabs   = document.querySelectorAll('.demo-tab');
  const panels = document.querySelectorAll('.demo-panel');
  if (!tabs.length) return;

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      const target = tab.dataset.demo;
      tabs.forEach(t => t.classList.toggle('active', t === tab));
      panels.forEach(p => p.classList.toggle('active', p.dataset.demo === target));
    });
  });
  // activate first
  tabs[0]?.classList.add('active');
  panels[0]?.classList.add('active');
}

/* ── Terminal typewriter ───────────────────────────────────────────────── */
function initTypewriter() {
  const el = document.getElementById('tw-cursor');
  if (!el) return;
  const lines = [
    { prompt: true,  text: 'molt init myapp' },
    { prompt: false, text: '✓ scaffolded myapp/', cls: 'term-success' },
    { prompt: true,  text: 'cd myapp && molt add fastapi uvicorn' },
    { prompt: false, text: '→ syncing 3 packages into ~/.molt/pkg/', cls: 'term-out' },
    { prompt: false, text: '✓ fastapi  0.111.0  (py3-none-any)', cls: 'term-success' },
    { prompt: false, text: '✓ uvicorn  0.29.0   (py3-none-any)', cls: 'term-success' },
    { prompt: true,  text: 'molt run dev' },
    { prompt: false, text: 'INFO:     Started server process', cls: 'term-out' },
    { prompt: false, text: 'INFO:     Uvicorn running on http://127.0.0.1:8000', cls: 'term-success' },
  ];

  const body = document.getElementById('tw-body');
  if (!body) return;

  let li = 0, ci = 0;
  const CHAR_DELAY = 38, LINE_DELAY = 420, PAUSE = 800;

  function addCompletedLine(line) {
    const row = document.createElement('div');
    row.className = 'term-line';
    if (line.prompt) {
      row.innerHTML = `<span class="term-prompt">$</span><span class="term-cmd"> ${line.text}</span>`;
    } else {
      row.innerHTML = `<span class="${line.cls || 'term-out'}"> ${line.text}</span>`;
    }
    body.insertBefore(row, el.parentElement);
  }

  function typeNext() {
    if (li >= lines.length) {
      // loop
      setTimeout(() => {
        body.querySelectorAll('.term-line').forEach(r => r.remove());
        li = 0; ci = 0;
        typeNext();
      }, 3000);
      return;
    }
    const line = lines[li];
    if (!line.prompt) {
      addCompletedLine(line);
      li++; ci = 0;
      setTimeout(typeNext, LINE_DELAY);
      return;
    }
    const cursorRow = el.parentElement;
    let textEl = cursorRow.querySelector('.tw-text');
    if (!textEl) {
      textEl = document.createElement('span');
      textEl.className = 'tw-text term-cmd';
      cursorRow.insertBefore(textEl, el);
    }
    if (ci < line.text.length) {
      textEl.textContent += line.text[ci];
      ci++;
      setTimeout(typeNext, CHAR_DELAY);
    } else {
      // line complete: pause, then commit
      setTimeout(() => {
        addCompletedLine(line);
        textEl.textContent = '';
        li++; ci = 0;
        setTimeout(typeNext, ci === 0 ? PAUSE : LINE_DELAY);
      }, PAUSE);
    }
  }

  setTimeout(typeNext, 600);
}

/* ── Scroll-reveal ─────────────────────────────────────────────────────── */
function initReveal() {
  const els = document.querySelectorAll('[data-reveal]');
  if (!('IntersectionObserver' in window)) {
    els.forEach(el => el.style.opacity = 1);
    return;
  }
  const io = new IntersectionObserver((entries) => {
    entries.forEach(e => {
      if (e.isIntersecting) {
        e.target.classList.add('fade-up');
        io.unobserve(e.target);
      }
    });
  }, { threshold: .15 });
  els.forEach(el => {
    el.style.opacity = 0;
    io.observe(el);
  });
}

/* ── Copy button ───────────────────────────────────────────────────────── */
function initCopyButtons() {
  document.querySelectorAll('pre').forEach(pre => {
    const btn = document.createElement('button');
    btn.className = 'copy-btn';
    btn.textContent = 'copy';
    btn.style.cssText = `
      position:absolute; top:.5rem; right:.5rem;
      background:rgba(108,142,245,.15); border:1px solid rgba(108,142,245,.3);
      color:#94a3b8; border-radius:4px; padding:.2rem .5rem;
      font-size:.7rem; cursor:pointer; font-family:var(--font-mono);
      transition:all .2s;
    `;
    const wrap = document.createElement('div');
    wrap.style.position = 'relative';
    pre.parentNode.insertBefore(wrap, pre);
    wrap.appendChild(pre);
    wrap.appendChild(btn);
    btn.addEventListener('click', () => {
      navigator.clipboard.writeText(pre.textContent).then(() => {
        btn.textContent = 'copied!';
        btn.style.color = '#34d399';
        setTimeout(() => { btn.textContent = 'copy'; btn.style.color = ''; }, 1500);
      });
    });
  });
}

/* ── Init ──────────────────────────────────────────────────────────────── */
document.addEventListener('DOMContentLoaded', () => {
  initDemoTabs();
  initTypewriter();
  initReveal();
  initCopyButtons();
});
