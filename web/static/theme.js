// Runs before the page is drawn (no "defer"): applies the light/dark choice
// saved in this browser, so the page doesn't flash in the other colours.
// A separate file (not inline) so a strict Content-Security-Policy works.
try {
  var t = localStorage.getItem("qs-podscript-theme") || localStorage.getItem("podscribe-theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch (e) { /* storage blocked */ }
