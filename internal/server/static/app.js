// Radar: service worker registration and request feedback. Everything
// else is server-rendered and driven by htmx attributes.
"use strict";

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
}

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
  const status = e.detail.xhr.status;
  if (status === 401) return; // HX-Redirect to the login page handles it
  toast(status === 403 ? "That request was blocked." : "Something went wrong. Please try again.");
});
document.addEventListener("htmx:sendError", () => toast("You're offline. Try again when you're connected."));
