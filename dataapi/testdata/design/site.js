// source: claude-design eve-cyno/project/data-site.js etag 1792305978964503
/* EVE-CYNO DATA · data-site.js
   Behaviour for the portal and the docs. Vanilla, no dependencies, served verbatim.
   Every feature starts only when its data-* hooks are on the page:
     drawer   [data-drawer] [data-drawer-open] [data-drawer-close] [data-drawer-scrim]
     search   [data-search] [data-search-input] [data-search-results] [data-search-toggle] [data-search-close]
              index: <script type="application/json" data-search-index>[{t,u,k,tier,live,s}]</script>
                     live is the live badge of a tool ("live ESI", "live EvE-Scout", "live with options"); absent otherwise
     tabs     [data-tabs] (optional data-tab-group) > [role=tab][data-tab] + [data-panel]
     copy     [data-copy-scope] > [data-copy] (+ [data-copy-label]); [data-nocopy] text is skipped
     toc      details[data-toc] > a[href^="#"]   (scroll-spy + open on wide screens)
     filter   [data-filter-scope] > [data-filter] chips, .tool-row[data-tier][data-upstream], .tier-group
   No ids are required; nothing here is design-only. */
(function () {
  'use strict';
  var doc = document, root = doc.documentElement;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var store = {
    get: function (k) { try { return window.localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { window.localStorage.setItem(k, v); } catch (e) { /* storage off */ } }
  };
  var live = doc.createElement('div');
  live.className = 'sr-only';
  live.setAttribute('aria-live', 'polite');
  doc.body.appendChild(live);
  function say(msg) { live.textContent = ''; setTimeout(function () { live.textContent = msg; }, 30); }
  function typing(el) { return el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)); }

  /* ---------------- drawer (docs sidebar / landing menu) ---------------- */
  var drawer = $('[data-drawer]'), opener = $('[data-drawer-open]'), lastFocus = null;
  function drawerIsModal() { return drawer && getComputedStyle(drawer).position === 'fixed'; }
  function setDrawer(open) {
    if (!drawer) return;
    root.classList.toggle('drawer-open', open);
    if (opener) opener.setAttribute('aria-expanded', String(open));
    if (open) {
      lastFocus = doc.activeElement;
      var first = $('[data-drawer-close]', drawer) || $('a, button', drawer);
      if (first) setTimeout(function () { first.focus({ preventScroll: true }); }, 60);
    } else if (lastFocus && lastFocus.focus) {
      lastFocus.focus({ preventScroll: true });
    }
  }
  if (drawer) {
    if (opener) opener.addEventListener('click', function () { setDrawer(!root.classList.contains('drawer-open')); });
    $$('[data-drawer-close], [data-drawer-scrim]').forEach(function (b) { b.addEventListener('click', function () { setDrawer(false); }); });
    drawer.addEventListener('click', function (e) {
      var a = e.target.closest('a');
      if (a && drawerIsModal()) setDrawer(false);
    });
    window.addEventListener('resize', function () {
      if (root.classList.contains('drawer-open') && !drawerIsModal()) setDrawer(false);
    });
  }

  /* ---------------- search ---------------- */
  var search = $('[data-search]');
  var indexEl = $('[data-search-index]');
  var index = [];
  try { index = indexEl ? JSON.parse(indexEl.textContent) : []; } catch (e) { index = []; }
  if (search) {
    var input = $('[data-search-input]', search), box = $('[data-search-results]', search);
    var sel = -1, hits = [];
    var TIER = { 'public': 'public', 'keyed': 'keyed', 'byo-key': 'byo-key' };
    var score = function (item, q, words) {
      var t = item.t.toLowerCase(), s = (item.s || '').toLowerCase(), u = q.replace(/\s+/g, '_');
      var hay = t + ' ' + s + ' ' + (item.tier || '') + (item.live ? ' ' + String(item.live).toLowerCase() : '');
      for (var i = 0; i < words.length; i++) if (hay.indexOf(words[i]) < 0 && t.indexOf(words[i].replace(/\s+/g, '_')) < 0) return 0;
      if (t === q || t === u) return 100;
      if (t.indexOf(q) === 0 || t.indexOf(u) === 0) return 80;
      if (t.indexOf(q) >= 0 || t.indexOf(u) >= 0) return 60;
      if (words.every(function (w) { return t.indexOf(w) >= 0; })) return 45;
      return 20 + (item.k === 'page' ? 2 : 0);
    };
    var render = function () {
      box.textContent = '';
      sel = hits.length ? 0 : -1;
      if (!input.value.trim()) { close(); return; }
      if (!hits.length) {
        var none = doc.createElement('div');
        none.className = 'sr-empty';
        none.textContent = 'No tool or page matches "' + input.value.trim() + '".';
        box.appendChild(none);
      }
      hits.forEach(function (h, i) {
        var a = doc.createElement('a');
        a.className = 'sr-item'; a.href = h.u; a.setAttribute('role', 'option');
        a.setAttribute('data-kind', h.k); a.setAttribute('aria-selected', String(i === sel));
        var t = doc.createElement('span'); t.className = 'sr-title'; t.textContent = h.t; a.appendChild(t);
        var m = doc.createElement('span'); m.className = 'sr-meta';
        if (h.live) { var l = doc.createElement('span'); l.className = 'badge badge-live'; l.textContent = String(h.live); m.appendChild(l); }
        if (h.tier && TIER[h.tier]) { var b = doc.createElement('span'); b.className = 'badge'; b.setAttribute('data-tier', h.tier); b.textContent = h.tier; m.appendChild(b); }
        else { var k = doc.createElement('span'); k.className = 'sr-kind'; k.textContent = h.k === 'tool' ? 'tool' : 'page'; m.appendChild(k); }
        a.appendChild(m);
        if (h.s) { var s = doc.createElement('span'); s.className = 'sr-sum'; s.textContent = h.s; a.appendChild(s); }
        a.addEventListener('mousemove', function () { move(i, true); });
        box.appendChild(a);
      });
      var foot = doc.createElement('div'); foot.className = 'sr-foot';
      foot.textContent = hits.length ? hits.length + (hits.length === 1 ? ' result' : ' results') + ' · ↑ ↓ to move · Enter to open' : 'Try a tool name, "fit", "price" or "limits".';
      box.appendChild(foot);
      box.hidden = false; input.setAttribute('aria-expanded', 'true');
    };
    var run = function () {
      var q = input.value.trim().toLowerCase();
      var words = q.split(/\s+/).filter(Boolean);
      hits = !q ? [] : index.map(function (it) { return { it: it, sc: score(it, q, words) }; })
        .filter(function (x) { return x.sc > 0; })
        .sort(function (a, b) { return b.sc - a.sc || a.it.t.localeCompare(b.it.t); })
        .slice(0, 8).map(function (x) { return x.it; });
      render();
    };
    var move = function (i, quiet) {
      var items = $$('.sr-item', box);
      if (!items.length) return;
      sel = (i + items.length) % items.length;
      items.forEach(function (el, j) { el.setAttribute('aria-selected', String(j === sel)); });
      if (!quiet) items[sel].scrollIntoView({ block: 'nearest' });
    };
    var close = function () { box.hidden = true; input.setAttribute('aria-expanded', 'false'); };
    var openOverlay = function () { root.classList.add('search-open'); setTimeout(function () { input.focus(); }, 30); };
    var closeOverlay = function () { root.classList.remove('search-open'); close(); };

    input.addEventListener('input', run);
    input.addEventListener('focus', function () { if (input.value.trim()) run(); });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowDown') { e.preventDefault(); move(sel + 1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); move(sel - 1); }
      else if (e.key === 'Enter') {
        var items = $$('.sr-item', box);
        if (items[sel]) { e.preventDefault(); items[sel].click(); }
      } else if (e.key === 'Escape') {
        if (input.value) { input.value = ''; close(); } else { input.blur(); closeOverlay(); }
      }
    });
    box.addEventListener('click', function (e) {
      if (e.target.closest('.sr-item')) { input.value = ''; close(); root.classList.remove('search-open'); input.blur(); }
    });
    doc.addEventListener('click', function (e) { if (!search.contains(e.target) && !e.target.closest('[data-search-toggle]')) close(); });
    $$('[data-search-toggle]').forEach(function (b) { b.addEventListener('click', openOverlay); });
    $$('[data-search-close]', search).forEach(function (b) { b.addEventListener('click', function () { input.value = ''; closeOverlay(); }); });
    doc.addEventListener('keydown', function (e) {
      var k = e.key;
      if ((k === '/' && !typing(doc.activeElement)) || (k.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey))) {
        e.preventDefault();
        if (getComputedStyle(search).display === 'none') openOverlay(); else input.focus();
      }
    });
  }
  doc.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && root.classList.contains('drawer-open')) setDrawer(false);
  });

  /* ---------------- tabs (optionally synced per data-tab-group, remembered) ---------------- */
  var tabSets = $$('[data-tabs]');
  function tabsOf(set) { return $$('[role="tab"][data-tab]', set).filter(function (t) { return t.closest('[data-tabs]') === set; }); }
  function selectTab(set, value, focus) {
    var tabs = tabsOf(set), hit = null;
    tabs.forEach(function (t) { if (t.getAttribute('data-tab') === value) hit = t; });
    if (!hit) return false;
    tabs.forEach(function (t) {
      var on = t === hit;
      t.setAttribute('aria-selected', String(on));
      t.tabIndex = on ? 0 : -1;
    });
    $$('[data-panel]', set).forEach(function (p) { if (p.closest('[data-tabs]') === set) p.hidden = p.getAttribute('data-panel') !== value; });
    if (focus) hit.focus();
    return true;
  }
  function selectGroup(group, value) {
    tabSets.forEach(function (s) { if (s.getAttribute('data-tab-group') === group) selectTab(s, value); });
    store.set('eve-cyno-data:tab:' + group, value);
  }
  tabSets.forEach(function (set) {
    var group = set.getAttribute('data-tab-group');
    var saved = group && store.get('eve-cyno-data:tab:' + group);
    if (saved) selectTab(set, saved);
    tabsOf(set).forEach(function (tab, i, all) {
      tab.addEventListener('click', function () {
        var v = tab.getAttribute('data-tab');
        if (group) selectGroup(group, v); else selectTab(set, v);
      });
      tab.addEventListener('keydown', function (e) {
        var n = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? all.length - 1 : null;
        if (n === null) return;
        e.preventDefault();
        var next = all[(n + all.length) % all.length], v = next.getAttribute('data-tab');
        if (group) selectGroup(group, v);
        selectTab(set, v, true);
      });
    });
  });

  /* ---------------- copy ---------------- */
  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    return new Promise(function (ok, fail) {
      var ta = doc.createElement('textarea');
      ta.value = text; ta.setAttribute('readonly', ''); ta.style.position = 'fixed'; ta.style.opacity = '0';
      doc.body.appendChild(ta); ta.select();
      try { doc.execCommand('copy') ? ok() : fail(); } catch (e) { fail(e); }
      doc.body.removeChild(ta);
    });
  }
  doc.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-copy]');
    if (!btn) return;
    var scope = btn.closest('[data-copy-scope]');
    var pre = scope && $$('pre', scope).filter(function (p) { return !p.hidden; })[0];
    if (!pre) return;
    var clone = pre.cloneNode(true);
    $$('[data-nocopy]', clone).forEach(function (n) { n.parentNode.removeChild(n); });
    var label = $('[data-copy-label]', btn);
    copyText(clone.textContent.replace(/\s+$/, '')).then(function () {
      btn.classList.add('is-done');
      if (label) label.textContent = 'Copied';
      say('Copied to clipboard');
      clearTimeout(btn._t);
      btn._t = setTimeout(function () { btn.classList.remove('is-done'); if (label) label.textContent = 'Copy'; }, 1600);
    }, function () { say('Copy failed: select the text instead'); });
  });

  /* ---------------- "On this page" (open on wide screens + scroll-spy) ---------------- */
  var tocs = $$('[data-toc]');
  var wide = window.matchMedia('(min-width: 1181px)');
  function syncTocOpen() { tocs.forEach(function (t) { if (t.tagName === 'DETAILS') t.open = wide.matches; }); }
  if (tocs.length) {
    syncTocOpen();
    if (wide.addEventListener) wide.addEventListener('change', syncTocOpen);
    tocs.forEach(function (t) {
      var sum = $('summary', t);
      if (sum) sum.addEventListener('click', function (e) { if (wide.matches) e.preventDefault(); });
    });
    var ticking = false;
    var spy = function () {
      ticking = false;
      var line = (parseInt(getComputedStyle(root).getPropertyValue('--top-h'), 10) || 60) + 48;
      tocs.forEach(function (t) {
        if (!t.offsetParent && t.getClientRects().length === 0) return;
        var links = $$('a[href^="#"]', t), active = null;
        links.forEach(function (a) {
          var el = doc.getElementById(decodeURIComponent(a.getAttribute('href').slice(1)));
          if (el && el.getClientRects().length && el.getBoundingClientRect().top <= line) active = a;
        });
        if (!active) active = links[0];
        if ((window.innerHeight + window.scrollY) >= doc.body.scrollHeight - 4) active = links[links.length - 1] || active;
        links.forEach(function (a) { a.classList.toggle('is-active', a === active); });
      });
    };
    var onScroll = function () { if (!ticking) { ticking = true; requestAnimationFrame(spy); } };
    window.addEventListener('scroll', onScroll, { passive: true });
    window.addEventListener('resize', onScroll);
    window.addEventListener('hashchange', function () { setTimeout(spy, 60); });
    spy();
  }

  /* ---------------- tier filter chips ---------------- */
  $$('[data-filter-scope]').forEach(function (scope) {
    var chips = $$('[data-filter]', scope);
    chips.forEach(function (c) {
      c.addEventListener('click', function () {
        var f = c.getAttribute('data-filter');
        chips.forEach(function (x) { x.setAttribute('aria-pressed', String(x === c)); });
        $$('.tool-row', scope).forEach(function (row) {
          var show = f === 'all' || (f === 'live' ? row.getAttribute('data-upstream') === 'true' : row.getAttribute('data-tier') === f);
          row.hidden = !show;
        });
        $$('.tier-group', scope).forEach(function (g) { g.hidden = !$$('.tool-row', g).some(function (r) { return !r.hidden; }); });
      });
    });
  });

  /* close transient UI when the page (or the demo page state) changes */
  window.addEventListener('hashchange', function () {
    if (search) { var b = $('[data-search-results]', search); if (b) b.hidden = true; root.classList.remove('search-open'); }
  });
})();
