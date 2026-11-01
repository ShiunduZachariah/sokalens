/* SokaLens design export: small runtime for the static pages.
   - theme toggle (remembered in localStorage)
   - ?ads=empty collapses ad slots
   - adhesion ad close button
   - buttons with data-href open the matching exported state (match tabs, Builder modes)
   - favourite hearts toggle (local only) */
(function () {
  var root = document.documentElement;
  function setTheme(t) { root.dataset.theme = t; try { localStorage.setItem('sl-theme', t); } catch (e) {} }
  var params = new URLSearchParams(location.search);
  if (params.get('theme')) setTheme(params.get('theme'));
  if (params.get('ads') === 'empty') root.dataset.ads = 'empty';
  document.addEventListener('click', function (e) {
    var t = e.target.closest('[data-sl-theme-toggle]');
    if (t) { setTheme(root.dataset.theme === 'light' ? 'dark' : 'light'); return; }
    var c = e.target.closest('button[aria-label="Close advertisement"]');
    if (c) { var slot = c.closest('[data-ad-slot]'); if (slot) slot.classList.add('sl-ad-closed'); return; }
    var f = e.target.closest('button[aria-pressed][aria-label*="favourites"]');
    if (f) { var on = f.getAttribute('aria-pressed') !== 'true'; f.setAttribute('aria-pressed', on);
      f.style.color = on ? 'var(--fav)' : 'var(--faint)'; f.style.fill = on ? 'var(--fav)' : 'none';
      var l = f.getAttribute('aria-label').replace(/^(Add|Remove) /, '').replace(/ (to|from) favourites$/, '');
      f.setAttribute('aria-label', (on ? 'Remove ' : 'Add ') + l + (on ? ' from favourites' : ' to favourites')); f.title = f.getAttribute('aria-label'); return; }
    var h = e.target.closest('[data-href]');
    if (h) { location.href = h.getAttribute('data-href') + location.search; }
  });
  // keep ?ads=empty / ?theme while browsing between pages
  if (location.search) document.querySelectorAll('a[href$=".html"]').forEach(function (a) { a.href = a.getAttribute('href') + location.search; });
})();
