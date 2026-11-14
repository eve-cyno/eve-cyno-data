(function () {
  var w = document.querySelector('.cy-wm.cy-play');
  if (!w) { return; }
  try {
    if (sessionStorage.getItem('cy-wm-seen')) { w.classList.remove('cy-play'); return; }
    sessionStorage.setItem('cy-wm-seen', '1');
  } catch (e) { }
})();
