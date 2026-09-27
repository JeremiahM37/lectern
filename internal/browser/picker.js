// Lectern Design Mode picker (docs/browser.md). Hover highlights an element and
// shows its DOM path; a click selects it (Shift adds to the selection); Escape
// ends picking. A selection is described as trimmed HTML, the CSS that differs
// from the element's defaults, a selector path and its rectangle.
//
// It is injected only while Design Mode is on: by the Browser pane's proxy into
// that dev server's own HTML ("frame": reports to the pane with postMessage),
// or by DevTools into the shared browser ("cdp": reports through a binding).
(function () {
  "use strict";
  var cfg = window.__lecternDesignConfig || {};
  if (window.__lecternDesign) {
    if (cfg.mode === "cdp" || cfg.mode === "frame") window.__lecternDesign.enable(cfg);
    return;
  }
  var MAX_HTML = 6000, MAX_TEXT = 200, MAX_DEPTH = 4, MAX_CHILDREN = 12;
  var PROPS = [
    "display", "position", "top", "right", "bottom", "left", "z-index", "float", "box-sizing",
    "width", "height", "min-width", "max-width", "min-height", "max-height",
    "margin-top", "margin-right", "margin-bottom", "margin-left",
    "padding-top", "padding-right", "padding-bottom", "padding-left",
    "flex-direction", "flex-wrap", "flex-grow", "flex-shrink", "flex-basis", "order",
    "justify-content", "align-items", "align-self", "align-content", "gap", "row-gap", "column-gap",
    "grid-template-columns", "grid-template-rows", "grid-column", "grid-row",
    "overflow-x", "overflow-y",
    "font-family", "font-size", "font-weight", "font-style", "line-height", "letter-spacing",
    "text-align", "text-transform", "text-decoration-line", "white-space", "color",
    "background-color", "background-image",
    "border-top-width", "border-right-width", "border-bottom-width", "border-left-width",
    "border-top-style", "border-right-style", "border-bottom-style", "border-left-style",
    "border-top-color", "border-right-color", "border-bottom-color", "border-left-color",
    "border-top-left-radius", "border-top-right-radius", "border-bottom-right-radius", "border-bottom-left-radius",
    "box-shadow", "opacity", "transform", "transition", "cursor", "outline-style", "visibility"
  ];
  var host = null, box = null, label = null, active = false, mode = cfg.mode, parentOrigin = cfg.parentOrigin || "";
  var defaultsFrame = null, defaultsCache = {};

  function isOurs(el) { return host && (el === host || host.contains(el)); }

  function segment(el) {
    var s = el.tagName.toLowerCase();
    if (el.id) return s + "#" + el.id;
    var classes = (typeof el.className === "string" ? el.className : "").trim().split(/\s+/).filter(function (c) {
      return c && c.length < 40 && !/\d{3,}|^css-|^sc-|^_/.test(c);
    }).slice(0, 2);
    return s + (classes.length ? "." + classes.join(".") : "");
  }

  function breadcrumb(el) {
    var parts = [];
    for (var n = el; n && n.nodeType === 1 && parts.length < 8; n = n.parentElement) parts.unshift(segment(n));
    return parts.join(" › ");
  }

  function esc(v) { return window.CSS && CSS.escape ? CSS.escape(v) : v.replace(/[^a-zA-Z0-9_-]/g, "\\$&"); }

  // A selector that matches exactly this element, as short as it can be.
  function selectorPath(el) {
    if (el.id && document.querySelectorAll("#" + esc(el.id)).length === 1) return "#" + esc(el.id);
    var parts = [];
    for (var n = el; n && n.nodeType === 1 && n !== document.documentElement; n = n.parentElement) {
      var part = n.tagName.toLowerCase();
      if (n.id && document.querySelectorAll("#" + esc(n.id)).length === 1) { parts.unshift("#" + esc(n.id)); break; }
      var parent = n.parentElement;
      if (parent) {
        var same = Array.prototype.filter.call(parent.children, function (c) { return c.tagName === n.tagName; });
        if (same.length > 1) part += ":nth-of-type(" + (same.indexOf(n) + 1) + ")";
      }
      parts.unshift(part);
      var sel = parts.join(" > ");
      try { if (document.querySelectorAll(sel).length === 1) return sel; } catch (e) { /* keep climbing */ }
    }
    return parts.join(" > ");
  }

  function trimAttr(v) {
    if (/^data:/i.test(v) && v.length > 80) return v.slice(0, 40) + "…(trimmed)";
    return v.length > 300 ? v.slice(0, 300) + "…" : v;
  }

  // The element's markup, small enough to read: scripts and SVG paths
  // dropped, secrets blanked, deep or long subtrees summarised.
  function trimHTML(el) {
    var clone = el.cloneNode(true);
    function walk(node, depth) {
      if (node.nodeType === 3) {
        var t = node.nodeValue;
        if (t.length > MAX_TEXT) node.nodeValue = t.slice(0, MAX_TEXT) + "…";
        return;
      }
      if (node.nodeType !== 1) return;
      var tag = node.tagName.toLowerCase();
      for (var i = node.attributes.length - 1; i >= 0; i--) {
        var a = node.attributes[i];
        if (/^on/i.test(a.name)) node.removeAttribute(a.name);
        else node.setAttribute(a.name, trimAttr(a.value));
      }
      if (tag === "input" && /password|hidden/i.test(node.getAttribute("type") || "")) node.setAttribute("value", "");
      if (tag === "script" || tag === "style" || tag === "noscript" || tag === "template") { node.textContent = ""; return; }
      if (tag === "svg" && node.children.length) {
        node.innerHTML = "";
        node.appendChild(document.createComment(" svg contents trimmed "));
        return;
      }
      var kids = Array.prototype.slice.call(node.childNodes);
      if (depth >= MAX_DEPTH && node.children.length) {
        var n = node.children.length;
        node.innerHTML = "";
        node.appendChild(document.createComment(" " + n + " child element" + (n === 1 ? "" : "s") + " trimmed "));
        return;
      }
      var seen = 0;
      kids.forEach(function (k) {
        if (k.nodeType === 1 && ++seen > MAX_CHILDREN) node.removeChild(k);
        else walk(k, depth + 1);
      });
      if (seen > MAX_CHILDREN) node.appendChild(document.createComment(" " + (seen - MAX_CHILDREN) + " more trimmed "));
    }
    walk(clone, 0);
    var html = clone.outerHTML, original = el.outerHTML.length;
    var truncated = html.length > MAX_HTML || html.length < original;
    if (html.length > MAX_HTML) html = html.slice(0, MAX_HTML) + "\n<!-- trimmed: " + (html.length - MAX_HTML) + " more characters -->";
    return { html: html, truncated: truncated, original_length: original };
  }

  function defaultsFor(tag) {
    if (defaultsCache[tag]) return defaultsCache[tag];
    var values = null;
    try {
      if (!defaultsFrame) {
        defaultsFrame = document.createElement("iframe");
        defaultsFrame.setAttribute("aria-hidden", "true");
        defaultsFrame.style.cssText = "position:fixed;width:0;height:0;border:0;visibility:hidden;left:-9999px";
        (document.body || document.documentElement).appendChild(defaultsFrame);
      }
      var doc = defaultsFrame.contentDocument, probe = doc.createElement(tag);
      doc.body.appendChild(probe);
      var cs = defaultsFrame.contentWindow.getComputedStyle(probe);
      values = {};
      PROPS.forEach(function (p) { values[p] = cs.getPropertyValue(p); });
      probe.remove();
    } catch (e) { values = null; }
    defaultsCache[tag] = values;
    return values;
  }

  var NEUTRAL = /^(none|normal|auto|0px|0s|0|static|visible|baseline|rgba\(0, 0, 0, 0\)|start|stretch|nowrap|row|1|0px 0px|content-box|repeat)$/;

  // Computed values that differ from the same element with no styling at all.
  function styleDiff(el) {
    var cs = getComputedStyle(el), base = defaultsFor(el.tagName.toLowerCase()), out = {};
    PROPS.forEach(function (p) {
      var v = cs.getPropertyValue(p);
      if (!v) return;
      if (p === "width" || p === "height") { out[p] = v; return; }
      if (base ? base[p] !== v : !NEUTRAL.test(v)) out[p] = v;
    });
    ["top", "right", "bottom", "left"].forEach(function (s) {
      if (out["border-" + s + "-width"] === undefined) { delete out["border-" + s + "-color"]; delete out["border-" + s + "-style"]; }
    });
    return out;
  }

  // A positioned element computes every offset, including the ones nobody
  // wrote (right: 1070px). Keep only those its own rules or style name.
  function dropDerivedOffsets(css, el, rules) {
    var authored = (el.getAttribute("style") || "") + " " + rules.join(" ");
    ["top", "right", "bottom", "left"].forEach(function (p) {
      if (css[p] !== undefined && !new RegExp("(^|[\\s;{])" + p + "\\s*:").test(authored) && !/\binset\s*:/.test(authored)) delete css[p];
    });
    return css;
  }

  // Author rules that match the element, from stylesheets this page may read.
  function matchedRules(el) {
    var rules = [];
    Array.prototype.forEach.call(document.styleSheets, function (sheet) {
      var list;
      try { list = sheet.cssRules; } catch (e) { return; }
      (function scan(items) {
        Array.prototype.forEach.call(items || [], function (r) {
          if (rules.length >= 12) return;
          if (r.cssRules && !r.selectorText) { scan(r.cssRules); return; }
          try {
            if (r.selectorText && el.matches(r.selectorText)) {
              var text = r.cssText;
              rules.push(text.length > 400 ? text.slice(0, 400) + "…" : text);
            }
          } catch (e) { /* a selector this engine cannot match */ }
        });
      })(list);
    });
    return rules;
  }

  // Where the element comes from in the app's source, when a dev build says:
  // React's debug info, Vue's component file, Svelte's location, or the
  // data attributes inspector plugins add.
  function sourceOf(el) {
    for (var n = el, hops = 0; n && n.nodeType === 1 && hops < 12; n = n.parentElement, hops++) {
      var file = n.getAttribute("data-inspector-relative-path") || n.getAttribute("data-source-file");
      if (file) return { file: file, line: +(n.getAttribute("data-inspector-line") || n.getAttribute("data-source-line") || 0), via: "data attribute" };
      if (n.__svelte_meta && n.__svelte_meta.loc) {
        var loc = n.__svelte_meta.loc;
        return { file: loc.file, line: loc.line + 1, column: loc.column, via: "svelte" };
      }
      if (n.__vueParentComponent && n.__vueParentComponent.type && n.__vueParentComponent.type.__file) {
        return { file: n.__vueParentComponent.type.__file, via: "vue" };
      }
      var key = Object.keys(n).find(function (k) { return k.indexOf("__reactFiber$") === 0; });
      for (var f = key && n[key], up = 0; f && up < 20; f = f.return, up++) {
        if (f._debugSource && f._debugSource.fileName) {
          return { file: f._debugSource.fileName, line: f._debugSource.lineNumber, column: f._debugSource.columnNumber, via: "react" };
        }
        var stack = f._debugStack && f._debugStack.stack;
        if (typeof stack === "string") {
          var lines = stack.split("\n");
          for (var i = 0; i < lines.length; i++) {
            var m = /(https?:\/\/[^\s)]+?):(\d+):(\d+)/.exec(lines[i]);
            if (m && !/node_modules|\/\.vite\/deps\/|react-dom|react\.development/.test(m[1])) {
              return { file: m[1].replace(/^https?:\/\/[^/]+/, "").replace(/\?.*$/, ""), line: +m[2], column: +m[3], via: "react" };
            }
          }
        }
      }
    }
    return null;
  }

  // The parent's markup, cut to two levels, so the agent sees what surrounds
  // the element.
  function neighborhood(el) {
    var parent = el.parentElement;
    if (!parent || parent === document.body || parent === document.documentElement) return "";
    var keep = [MAX_DEPTH, MAX_HTML];
    MAX_DEPTH = 2; MAX_HTML = 2000;
    try { return trimHTML(parent).html; } finally { MAX_DEPTH = keep[0]; MAX_HTML = keep[1]; }
  }

  function describe(el) {
    var r = el.getBoundingClientRect(), text = (el.innerText || el.textContent || "").trim().replace(/\s+/g, " ");
    var h = trimHTML(el), rules = matchedRules(el), css = styleDiff(el);
    if (rules.length || el.getAttribute("style")) dropDerivedOffsets(css, el, rules);
    return {
      selector: selectorPath(el),
      breadcrumb: breadcrumb(el),
      tag: el.tagName.toLowerCase(),
      text: text.length > 300 ? text.slice(0, 300) + "…" : text,
      html: h.html, html_truncated: h.truncated, html_length: h.original_length,
      css: css,
      rules: rules,
      source: sourceOf(el),
      context_html: neighborhood(el),
      rect: { x: r.left, y: r.top, width: r.width, height: r.height },
      scroll: { x: window.scrollX, y: window.scrollY },
      viewport: { width: window.innerWidth, height: window.innerHeight, dpr: window.devicePixelRatio || 1 },
      url: location.href,
      title: document.title
    };
  }

  function emit(msg) {
    if (mode === "cdp" && typeof window.__lecternDesignEmit === "function") window.__lecternDesignEmit(JSON.stringify(msg));
    else if (mode === "frame" && parentOrigin && window.parent !== window) window.parent.postMessage({ lecternDesign: msg }, parentOrigin);
  }

  function ensureOverlay() {
    if (host && host.isConnected) return;
    host = document.createElement("lectern-design-overlay");
    host.style.cssText = "position:fixed;inset:0;pointer-events:none;z-index:2147483647";
    var root = host.attachShadow ? host.attachShadow({ mode: "closed" }) : host;
    box = document.createElement("div");
    box.style.cssText = "position:fixed;border:2px solid #8b5cf6;background:rgba(139,92,246,.14);border-radius:2px;display:none;box-sizing:border-box";
    label = document.createElement("div");
    label.style.cssText = "position:fixed;max-width:90vw;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" +
      "font:12px/1.4 ui-monospace,monospace;background:#1e1b2e;color:#f5f3ff;padding:2px 6px;border-radius:4px;display:none";
    root.appendChild(box);
    root.appendChild(label);
    document.documentElement.appendChild(host);
  }

  function show(el) {
    ensureOverlay();
    var r = el.getBoundingClientRect();
    box.style.display = label.style.display = "block";
    box.style.left = r.left + "px"; box.style.top = r.top + "px";
    box.style.width = r.width + "px"; box.style.height = r.height + "px";
    label.textContent = breadcrumb(el) + "  " + Math.round(r.width) + "×" + Math.round(r.height);
    var top = r.top > 22 ? r.top - 22 : r.bottom + 4;
    label.style.left = Math.max(0, r.left) + "px"; label.style.top = Math.min(top, window.innerHeight - 20) + "px";
  }

  function hide() { if (box) { box.style.display = "none"; label.style.display = "none"; } }

  function pick(e) {
    var path = e.composedPath ? e.composedPath() : [e.target];
    for (var i = 0; i < path.length; i++) {
      var n = path[i];
      if (n && n.nodeType === 1 && !isOurs(n)) return n;
    }
    return null;
  }

  var current = null;
  function onMove(e) {
    var el = pick(e);
    if (!el || el === current) return;
    current = el;
    show(el);
    emit({ type: "hover", breadcrumb: breadcrumb(el) });
  }
  function swallow(e) { e.preventDefault(); e.stopPropagation(); if (e.stopImmediatePropagation) e.stopImmediatePropagation(); }
  function onClick(e) {
    swallow(e);
    var el = pick(e);
    if (!el) return;
    emit({ type: "select", additive: !!e.shiftKey, element: describe(el) });
  }
  function onKey(e) { if (e.key === "Escape") { swallow(e); emit({ type: "cancel" }); } }
  var EVENTS = [["mousemove", onMove], ["pointerdown", swallow], ["mousedown", swallow], ["pointerup", swallow],
    ["mouseup", swallow], ["click", onClick], ["dblclick", swallow], ["keydown", onKey]];

  function enable(next) {
    if (next) { mode = next.mode || mode; parentOrigin = next.parentOrigin || parentOrigin; }
    if (active) return;
    active = true;
    EVENTS.forEach(function (p) { window.addEventListener(p[0], p[1], true); });
  }
  function disable() {
    active = false;
    EVENTS.forEach(function (p) { window.removeEventListener(p[0], p[1], true); });
    hide();
    if (host) { host.remove(); host = null; }
    current = null;
  }

  // Draw the element with its computed styles into a canvas — the fallback
  // when no headless browser can render the page. Approximate: cross-origin
  // images and fonts are left out.
  function capture(el) {
    var r = el.getBoundingClientRect(), dpr = window.devicePixelRatio || 1;
    var clone = el.cloneNode(true), count = 0;
    function inline(src, dst) {
      if (src.nodeType !== 1 || ++count > 400) return;
      var cs = getComputedStyle(src), text = "";
      for (var i = 0; i < cs.length; i++) text += cs[i] + ":" + cs.getPropertyValue(cs[i]) + ";";
      dst.setAttribute("style", text);
      if (dst.tagName === "IMG" || dst.tagName === "VIDEO" || dst.tagName === "CANVAS" || dst.tagName === "IFRAME") {
        dst.removeAttribute("src"); dst.removeAttribute("srcset");
      }
      for (var j = 0; j < src.children.length && j < dst.children.length; j++) inline(src.children[j], dst.children[j]);
    }
    inline(el, clone);
    clone.style.margin = "0";
    var xml = new XMLSerializer().serializeToString(clone);
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" width="' + r.width + '" height="' + r.height + '">' +
      '<foreignObject width="100%" height="100%"><div xmlns="http://www.w3.org/1999/xhtml">' + xml + "</div></foreignObject></svg>";
    return new Promise(function (resolve, reject) {
      var img = new Image();
      img.onload = function () {
        try {
          var c = document.createElement("canvas");
          c.width = Math.max(1, Math.round(r.width * dpr)); c.height = Math.max(1, Math.round(r.height * dpr));
          var ctx = c.getContext("2d");
          ctx.scale(dpr, dpr);
          ctx.drawImage(img, 0, 0);
          resolve(c.toDataURL("image/png"));
        } catch (e) { reject(e); }
      };
      img.onerror = function () { reject(new Error("the element could not be drawn")); };
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
    });
  }

  window.addEventListener("message", function (e) {
    if (mode !== "frame" || e.source !== window.parent || e.origin !== parentOrigin) return;
    var msg = e.data && e.data.lecternDesign;
    if (!msg) return;
    if (msg.type === "capture") {
      var el = null;
      try { el = document.querySelector(msg.selector); } catch (err) { el = null; }
      if (!el) { emit({ type: "capture", id: msg.id, error: "element not found" }); return; }
      hide();
      capture(el).then(function (png) { emit({ type: "capture", id: msg.id, png: png }); },
        function (err) { emit({ type: "capture", id: msg.id, error: String(err && err.message || err) }); });
    } else if (msg.type === "disable") disable();
    else if (msg.type === "enable") enable();
  });

  window.__lecternDesign = { enable: enable, disable: disable, hide: hide, describe: describe, capture: capture };
  if (mode === "cdp" || mode === "frame") enable();
})();
