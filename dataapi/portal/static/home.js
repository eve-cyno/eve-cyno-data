// Shared scene host: lazy start in view, DPR cap, pause when hidden or off screen, one still frame for reduced motion.
window.CS = {
  reduced: matchMedia('(prefers-reduced-motion: reduce)').matches,
  css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); },
  glow() {
    const c = document.createElement('canvas'); c.width = c.height = 128;
    const g = c.getContext('2d'), r = g.createRadialGradient(64, 64, 0, 64, 64, 64);
    r.addColorStop(0, 'rgba(255,255,255,1)'); r.addColorStop(.25, 'rgba(255,255,255,.5)'); r.addColorStop(1, 'rgba(255,255,255,0)');
    g.fillStyle = r; g.fillRect(0, 0, 128, 128);
    return new THREE.CanvasTexture(c);
  },
  mount(canvas, setup) {
    if (!window.THREE || !canvas) return;
    let api = null, started = false, running = false, vis = false, raf = 0, t = 0, t0 = 0;
    const still = () => { if (api) api.frame(api.still == null ? 6 : api.still, 0); };
    const resize = () => {
      if (!api) return;
      const w = canvas.clientWidth, h = canvas.clientHeight; if (!w || !h) return;
      api.renderer.setSize(w, h, false); api.camera.aspect = w / h; api.camera.updateProjectionMatrix();
      if (api.resize) api.resize(w, h);
      if (!running) still();
    };
    const start = () => {
      started = true;
      try {
        const r = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: 'low-power' });
        r.setPixelRatio(Math.min(window.devicePixelRatio || 1, 1.5)); r.setClearColor(0x000000, 0);
        api = setup(r); api.renderer = r; resize();
      } catch (e) { api = null; canvas.hidden = true; }
    };
    const loop = (now) => {
      if (!running) return;
      const dt = Math.min(.05, (now - t0) / 1000); t0 = now; t += dt;
      api.frame(t, dt); raf = requestAnimationFrame(loop);
    };
    const sync = () => {
      const want = !!api && vis && !document.hidden && !CS.reduced;
      if (want && !running) { running = true; t0 = performance.now(); raf = requestAnimationFrame(loop); }
      else if (!want && running) { running = false; cancelAnimationFrame(raf); }
    };
    new IntersectionObserver((es) => { vis = es[0].isIntersecting; if (vis && !started) start(); sync(); }, { rootMargin: '120px' }).observe(canvas);
    document.addEventListener('visibilitychange', sync);
    addEventListener('resize', resize);
    if (CS.reduced) addEventListener('scroll', () => { if (vis) still(); }, { passive: true });
  }
};
document.querySelectorAll('[data-copy]').forEach((b) => b.addEventListener('click', () => {
  const txt = document.getElementById(b.dataset.copy).textContent;
  const done = () => { const o = b.textContent; b.textContent = 'Copied'; setTimeout(() => { b.textContent = o; }, 1400); };
  try { navigator.clipboard.writeText(txt).then(done, () => {}); } catch (e) { /* selection stays manual */ }
}));


(() => {
  // Lobe names, counts and the total come from the page: .lobe h3 text, li[data-tool] per lobe, [data-slot="tools_offered"].
  const lobeEls = Array.from(document.querySelectorAll('#atlas .lobe'));
  const names = lobeEls.map((el) => {
    const h = el.querySelector('h3');
    return h ? Array.from(h.childNodes).filter((n) => n.nodeType === 3).map((n) => n.textContent).join('').trim() : '';
  });
  const counts = lobeEls.map((el) => el.querySelectorAll('li[data-tool]').length);
  const sum = counts.reduce((a, b) => a + b, 0);
  const slot = document.querySelector('[data-slot="tools_offered"]');
  const total = slot && /^\d+$/.test(slot.textContent.trim()) ? +slot.textContent.trim() : sum;
  const WORDS = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten'];
  const lobeWord = WORDS[lobeEls.length] || String(lobeEls.length);
  let hot = -1;
  const cap = document.getElementById('braincap');
  const brain = document.getElementById('brain');
  const idle = '<b>' + total + ' tools</b> \u00b7 ' + lobeWord + ' lobes \u00b7 hover a lobe below';
  if (cap) cap.innerHTML = idle;
  if (brain) brain.setAttribute('aria-label', 'A slowly turning graph of ' + total + ' tool nodes in ' + lobeWord + ' clusters, shaped like two hemispheres');
  const set = (g) => {
    hot = g;
    if (cap) cap.innerHTML = g < 0 ? idle : '<b>' + names[g] + '</b> \u00b7 ' + counts[g] + ' tools';
    if (window.__brainStill) window.__brainStill();
  };
  lobeEls.forEach((el, g) => {
    el.addEventListener('pointerenter', () => set(g)); el.addEventListener('focusin', () => set(g));
    el.addEventListener('pointerleave', () => set(-1)); el.addEventListener('focusout', () => set(-1));
  });
  CS.mount(brain, (r) => {
    const T = THREE, scene = new T.Scene(), camera = new T.PerspectiveCamera(40, 1, .1, 50);
    camera.position.set(0, .15, 3.9);
    const G = new T.Group(); scene.add(G);
    const glow = CS.glow(), green = new T.Color(CS.css('--c-green')), white = new T.Color(CS.css('--c-white-hot')), dimc = new T.Color(CS.css('--c-line-ctl'));
    const add = { blending: T.AdditiveBlending, transparent: true, depthWrite: false };
    let s = 7; const rnd = () => { s = (s * 16807) % 2147483647; return s / 2147483647; };
    // dim tissue: two hemispheres with a gap
    const nF = 620, fp = new Float32Array(nF * 3);
    for (let i = 0; i < nF; i++) {
      let x, y, z; do { x = rnd() * 2 - 1; y = rnd() * 2 - 1; z = rnd() * 2 - 1; } while (x * x + y * y + z * z > 1 || x * x + y * y + z * z < .55);
      x = x * 1.15 + Math.sign(x) * .09; y = y * .92 - Math.abs(x) * .1; z *= 1.3;
      fp.set([x, y, z], i * 3);
    }
    const fg = new T.BufferGeometry(); fg.setAttribute('position', new T.BufferAttribute(fp, 3));
    G.add(new T.Points(fg, new T.PointsMaterial({ color: dimc, size: .028, transparent: true, opacity: .8 })));
    const centres = [[-.72, .2, .62], [.72, .2, .62], [.62, -.42, -.3], [-.62, .42, -.5], [.42, .55, -.62], [-.58, -.45, -.05]];
    const lobes = [], edges = [];
    while (centres.length < counts.length) { const a = centres.length * 2.4; centres.push([Math.cos(a) * .7, Math.sin(a) * .5, Math.sin(a * 1.7) * .6]); }
    counts.forEach((_, g) => {
      const c = centres[g];
      const pts = [new T.Vector3(c[0], c[1], c[2])];
      for (let i = 0; i < counts[g]; i++) pts.push(new T.Vector3(c[0] + (rnd() - .5) * .62, c[1] + (rnd() - .5) * .62, c[2] + (rnd() - .5) * .62));
      const pg = new T.BufferGeometry().setFromPoints(pts.slice(1));
      const pm = new T.PointsMaterial(Object.assign({ color: green, size: .2, map: glow }, add));
      const lp = [];
      for (let i = 1; i < pts.length; i++) { lp.push(pts[0], pts[i]); edges.push([pts[0], pts[i], g]); const j = 1 + ((i) % (pts.length - 1)); if (j !== i) { lp.push(pts[i], pts[j]); edges.push([pts[i], pts[j], g]); } }
      const lm = new T.LineBasicMaterial(Object.assign({ color: green, opacity: .35 }, add));
      G.add(new T.Points(pg, pm), new T.LineSegments(new T.BufferGeometry().setFromPoints(lp), lm));
      lobes.push({ pm, lm, hub: pts[0] });
    });
    const cross = [[0, 1], [0, 3], [1, 4], [1, 2], [3, 4], [0, 5], [2, 5], [3, 5]].filter(([a, b]) => a < lobes.length && b < lobes.length), cp = [];
    cross.forEach(([a, b]) => { cp.push(lobes[a].hub, lobes[b].hub); edges.push([lobes[a].hub, lobes[b].hub, -1]); });
    G.add(new T.LineSegments(new T.BufferGeometry().setFromPoints(cp), new T.LineBasicMaterial(Object.assign({ color: dimc, opacity: .7 }, add))));
    const nP = 46, pp = new Float32Array(nP * 3), pulses = [];
    for (let i = 0; i < nP; i++) pulses.push({ e: Math.floor(rnd() * edges.length), k: rnd(), v: .25 + rnd() * .5 });
    const pgeo = new T.BufferGeometry(); pgeo.setAttribute('position', new T.BufferAttribute(pp, 3));
    G.add(new T.Points(pgeo, new T.PointsMaterial(Object.assign({ color: white, size: .09, map: glow }, add))));
    let px = 0, py = 0;
    r.domElement.addEventListener('pointermove', (e) => { const b = r.domElement.getBoundingClientRect(); px = (e.clientX - b.left) / b.width - .5; py = (e.clientY - b.top) / b.height - .5; });
    const api = {
      scene, camera, still: 3,
      frame(t, dt) {
        G.rotation.y = t * .16 + px * .9; G.rotation.x = .12 + py * .5;
        lobes.forEach((l, g) => { const on = hot < 0 || hot === g; l.pm.opacity = on ? 1 : .18; l.pm.size = hot === g ? .3 : .2; l.lm.opacity = on ? (hot === g ? .8 : .35) : .06; });
        for (let i = 0; i < nP; i++) {
          const q = pulses[i]; q.k += q.v * dt; if (q.k > 1) { q.k = 0; q.e = Math.floor(Math.random() * edges.length); }
          const e = edges[q.e]; pp[i * 3] = e[0].x + (e[1].x - e[0].x) * q.k; pp[i * 3 + 1] = e[0].y + (e[1].y - e[0].y) * q.k; pp[i * 3 + 2] = e[0].z + (e[1].z - e[0].z) * q.k;
        }
        pgeo.attributes.position.needsUpdate = true;
        r.render(scene, camera);
      }
    };
    if (CS.reduced) window.__brainStill = () => api.frame(3, 0);
    return api;
  });
})();

// Site search (reads the JSON index), example tabs, Hall of Fame filter (keyed on data-cat).
(() => {
  const input = document.getElementById('site-search'), box = document.getElementById('search-res');
  if (!input || !box) return;
  // The index is the live portal's: <script type="application/json" data-search-index>[{t,u,k,tier,live,s}]</script>
  let idx = [];
  try { const el = document.querySelector('[data-search-index]'); idx = el ? JSON.parse(el.textContent) : []; } catch (e) { idx = []; }
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const close = () => { box.hidden = true; input.setAttribute('aria-expanded', 'false'); };
  const render = () => {
    const q = input.value.trim().toLowerCase();
    if (!q) { close(); return; }
    const inTitle = (e) => (e.t.toLowerCase().indexOf(q) >= 0 ? 1 : 0);
    const hits = idx.filter((e) => (e.t + ' ' + e.s + ' ' + (e.live || '')).toLowerCase().indexOf(q) >= 0)
      .sort((a, b) => inTitle(b) - inTitle(a)).slice(0, 12);
    box.innerHTML = hits.length ? hits.map((e) =>
      '<a class="sr" href="' + esc(e.u) + '"><span class="sr-t">' + esc(e.t) +
      (e.k === 'page' ? '<span class="chip">page</span>' : '<span class="chip' + (e.tier === 'keyed' ? ' keyed' : '') + '">' + esc(e.tier) + '</span>') +
      (e.live ? '<span class="chip live">' + esc(e.live) + '</span>' : '') +
      '</span><span class="sr-s">' + esc(e.s) + '</span></a>').join('')
      : '<p class="sr-none">Nothing matches. Try a tool name such as get_fits, or a word such as price.</p>';
    box.hidden = false; input.setAttribute('aria-expanded', 'true');
  };
  input.addEventListener('input', render);
  input.addEventListener('focus', render);
  input.addEventListener('keydown', (e) => { if (e.key === 'Escape') { input.value = ''; close(); input.blur(); } });
  document.addEventListener('keydown', (e) => {
    const el = document.activeElement, tag = el ? el.tagName : '';
    if (e.key === '/' && tag !== 'INPUT' && tag !== 'TEXTAREA' && tag !== 'SELECT') { e.preventDefault(); input.focus(); }
  });
  document.addEventListener('pointerdown', (e) => { if (!e.target.closest('.search')) close(); });
  box.addEventListener('click', (e) => { if (e.target.closest('a')) close(); });
})();

document.querySelectorAll('[data-tabs]').forEach((root) => {
  const tabs = root.querySelectorAll('[role="tab"]'), copy = root.querySelector('[data-copy]');
  tabs.forEach((tab) => tab.addEventListener('click', () => {
    tabs.forEach((t) => {
      const on = t === tab, panel = document.getElementById(t.getAttribute('aria-controls'));
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      if (panel) panel.hidden = !on;
    });
    if (copy) copy.dataset.copy = tab.getAttribute('aria-controls');
  }));
});

(() => {
  const btns = document.querySelectorAll('.legend button'), tiles = document.querySelectorAll('.wall .tile');
  btns.forEach((b) => b.addEventListener('click', () => {
    const cat = b.dataset.cat || 'all';
    btns.forEach((x) => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
    tiles.forEach((t) => t.classList.toggle('off', cat !== 'all' && t.dataset.cat !== cat));
  }));
})();
