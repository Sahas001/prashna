// Radar: small enhancements on top of server-rendered pages and htmx.
"use strict";

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => navigator.serviceWorker.register("/sw.js").catch(() => {}));
}

// Opening a piece keeps the real link (so it can be copied or opened in a
// new tab as usual) and quietly tells Radar, which counts it as read.
function noteOpen(e) {
  const a = e.target.closest("a[data-open]");
  if (!a || (e.type === "auxclick" && e.button !== 1)) return;
  navigator.sendBeacon("/items/" + a.dataset.open + "/opened");
  const card = document.getElementById("item-" + a.dataset.open);
  if (card) card.classList.add("is-read");
}
document.addEventListener("click", noteOpen);
document.addEventListener("auxclick", noteOpen);

// Appearance: switch at once; the form also saves the choice (hx-post).
document.addEventListener("change", (e) => {
  if (e.target.name !== "theme" || !e.target.closest(".theme-picker")) return;
  const t = e.target.value;
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
});

let toastTimer;
function toast(msg) {
  const el = document.getElementById("toast");
  if (!el) return;
  el.textContent = msg;
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 3200);
}
document.addEventListener("htmx:responseError", (e) => {
  if (e.detail.xhr.status === 401) return; // HX-Redirect to sign-in handles it
  toast("That didn't work. Please try again.");
});
document.addEventListener("htmx:sendError", () => toast("Radar isn't reachable. Is it still running?"));
