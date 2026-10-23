// Runs before first paint so the page never flashes the wrong theme.
(function () {
  var t = null;
  try { t = localStorage.getItem("st-theme"); } catch (e) {}
  if (t !== "light" && t !== "dark") t = window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  document.documentElement.setAttribute("data-theme", t);
})();
