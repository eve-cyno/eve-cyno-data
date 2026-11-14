// source: claude-design-artifact version 1794617686-adcc project/eve-cyno/project/brand/wordmark-once.js sha256 cfeb32390622ba0f34b073ede417b7f3c195c9be1b20418c0c53a0c4219f6749
(function () {
  var w = document.querySelector('.cy-wm.cy-play');
  if (!w) { return; }
  try {
    if (sessionStorage.getItem('cy-wm-seen')) { w.classList.remove('cy-play'); return; }
    sessionStorage.setItem('cy-wm-seen', '1');
  } catch (e) { }
})();
