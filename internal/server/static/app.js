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
document.addEventListener("htmx:afterRequest", (e) => {
  if (e.detail.successful && e.detail.elt.matches("[data-saves]")) toast("Note saved");
});

// ---- Reader: highlights ----
// A highlight is a pair of character offsets into the article's text.
// The server keeps them; here they are drawn as <mark>s, which don't
// change the text, so offsets stay valid however many there are.

const articleEl = () => document.getElementById("article");

function textOffset(root, node, offset) {
  const r = document.createRange();
  r.selectNodeContents(root);
  r.setEnd(node, offset);
  return r.toString().length;
}

function markRange(root, start, end, id) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const hits = [];
  let pos = 0;
  for (let n = walker.nextNode(); n && pos < end; n = walker.nextNode()) {
    const len = n.data.length;
    if (pos + len > start) hits.push([n, Math.max(start - pos, 0), Math.min(end - pos, len)]);
    pos += len;
  }
  for (const [node, from, to] of hits) {
    let t = node;
    if (to < t.data.length) t.splitText(to);
    if (from > 0) t = t.splitText(from);
    if (!t.data.trim()) continue; // spacing between blocks
    const m = document.createElement("mark");
    m.className = "hl-mark";
    m.dataset.hl = id;
    t.replaceWith(m);
    m.append(t);
  }
}

function drawHighlights() {
  const root = articleEl();
  if (!root) return;
  root.querySelectorAll("mark.hl-mark").forEach((m) => m.replaceWith(...m.childNodes));
  root.normalize();
  const text = root.textContent;
  document.querySelectorAll("#highlights .hl").forEach((li) => {
    let start = Number(li.dataset.start), end = Number(li.dataset.end);
    const quote = li.querySelector(".hl-quote").textContent;
    if (text.slice(start, end) !== quote) {
      const at = text.indexOf(quote);
      if (at >= 0) { start = at; end = at + quote.length; }
      else if (end > text.length) return;
    }
    markRange(root, start, end, li.id.slice(3));
  });
}
document.addEventListener("DOMContentLoaded", drawHighlights);
document.addEventListener("htmx:afterSettle", drawHighlights);

function selectedRange() {
  const root = articleEl(), sel = document.getSelection();
  if (!root || !sel || sel.isCollapsed || !sel.rangeCount) return null;
  const r = sel.getRangeAt(0);
  if (!root.contains(r.startContainer) || !root.contains(r.endContainer) || !r.toString().trim()) return null;
  return r;
}

let hlButton, hlTimer;
function offerHighlight() {
  const r = selectedRange();
  if (!r) {
    if (hlButton) hlButton.hidden = true;
    return;
  }
  if (!hlButton) {
    hlButton = document.createElement("button");
    hlButton.type = "button";
    hlButton.className = "hl-button";
    hlButton.textContent = "Highlight";
    hlButton.addEventListener("pointerdown", (e) => e.preventDefault()); // keep the selection
    hlButton.addEventListener("click", saveHighlight);
  }
  if (!hlButton.isConnected) document.body.append(hlButton); // boosted navigation replaces the body
  hlButton.hidden = false;
  const rects = r.getClientRects();
  const last = rects.length ? rects[rects.length - 1] : r.getBoundingClientRect();
  const w = hlButton.offsetWidth;
  const left = Math.min(Math.max(last.right - w / 2, 8), document.documentElement.clientWidth - w - 8);
  hlButton.style.left = window.scrollX + left + "px";
  hlButton.style.top = window.scrollY + last.bottom + 10 + "px";
}
document.addEventListener("selectionchange", () => {
  clearTimeout(hlTimer);
  hlTimer = setTimeout(offerHighlight, 200);
});

function saveHighlight() {
  const r = selectedRange(), root = articleEl();
  if (!r) return;
  const text = root.textContent;
  let start = textOffset(root, r.startContainer, r.startOffset);
  let end = textOffset(root, r.endContainer, r.endOffset);
  while (start < end && /\s/.test(text[start])) start++;
  while (end > start && /\s/.test(text[end - 1])) end--;
  document.getSelection().removeAllRanges();
  hlButton.hidden = true;
  if (end <= start) return;
  if (end - start > 4000) return toast("That's too long to highlight. Pick a shorter passage.");
  htmx.ajax("POST", "/items/" + root.dataset.item + "/highlights", {
    target: "#highlights", swap: "outerHTML",
    values: { start: start, end: end, quote: text.slice(start, end) },
  });
}

// A highlight in the text and its entry in the list point at each other.
function flash(el) {
  if (!el) return;
  el.scrollIntoView({ block: "center", behavior: "smooth" });
  el.classList.add("flash");
  setTimeout(() => el.classList.remove("flash"), 1600);
}
document.addEventListener("click", (e) => {
  const mark = e.target.closest("mark.hl-mark");
  if (mark && !selectedRange()) return flash(document.getElementById("hl-" + mark.dataset.hl));
  const quote = e.target.closest("#highlights .hl-quote");
  if (quote) flash(document.querySelector('mark.hl-mark[data-hl="' + quote.closest(".hl").id.slice(3) + '"]'));
});
