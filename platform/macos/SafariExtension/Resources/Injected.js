(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.BlurPageCapture = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const DEFAULTS = Object.freeze({ maxNodes: 10000, maxBytes: 256 * 1024, budgetMs: 100 });
  const EXCLUDED = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'TEXTAREA', 'INPUT', 'SELECT', 'OPTION', 'IFRAME', 'OBJECT', 'EMBED']);

  function excluded(element, document, cache) {
    if (!element || element.nodeType !== 1) return false;
    if (cache.has(element)) return cache.get(element);
    let skip = EXCLUDED.has(element.tagName) || element.hidden === true || element.inert === true ||
      element.isContentEditable === true || element.getAttribute('aria-hidden') === 'true' ||
      element.hasAttribute('contenteditable') || element.hasAttribute('hidden') || element.hasAttribute('inert');
    if (!skip) {
      try {
        const style = document.defaultView.getComputedStyle(element);
        skip = style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse';
      } catch (_) { skip = true; }
    }
    cache.set(element, skip);
    return skip;
  }

  function extractText(root, limits) {
    const options = Object.assign({}, DEFAULTS, limits || {});
    const document = root && root.ownerDocument;
    if (!root || !document || !document.createTreeWalker || options.maxNodes < 1 || options.maxBytes < 1 || options.budgetMs < 1) {
      return { text: '', bytes: 0, nodes: 0, truncated: false };
    }
    const now = options.now || (() => (typeof performance !== 'undefined' ? performance.now() : Date.now()));
    const started = now();
    const filter = document.defaultView.NodeFilter;
    const accept = filter ? filter.FILTER_ACCEPT : 1;
    const reject = filter ? filter.FILTER_REJECT : 2;
    const show = filter ? filter.SHOW_ELEMENT | filter.SHOW_TEXT : 1 | 4;
    const hidden = new WeakMap();
    let nodes = 0;
    let bytes = 0;
    let truncated = false;
    const parts = [];
    const walker = document.createTreeWalker(root, show, {
      acceptNode(node) {
        if (node.nodeType === 1) return excluded(node, document, hidden) ? reject : accept;
        if (node.nodeType === 3) return excluded(node.parentElement, document, hidden) ? reject : accept;
        return reject;
      },
    });

    let node;
    while ((node = walker.nextNode())) {
      if (now() - started >= options.budgetMs || nodes >= options.maxNodes) { truncated = true; break; }
      nodes++;
      if (node.nodeType !== 3) continue;
      // Bound work before normalization; a hostile single text node cannot
      // force a whole-node regex/encoding allocation.
      const source = String(node.nodeValue || '').slice(0, options.maxBytes + 1);
      const value = source.replace(/[\t\n\r ]+/g, ' ').trim();
      if (!value) continue;
      const prefix = parts.length ? 1 : 0;
      const remaining = options.maxBytes - bytes - prefix;
      if (remaining <= 0) { truncated = true; break; }
      let piece = '';
      let size = 0;
      let scanned = 0;
      let consumed = 0;
      for (const character of value) {
        const width = utf8Bytes(character);
        if (size + width > remaining || (scanned & 255) === 0 && now() - started >= options.budgetMs) { truncated = true; break; }
        piece += character;
        size += width;
        consumed += character.length;
        scanned++;
      }
      if (piece) { parts.push(piece); bytes += prefix + size; }
      if (consumed < value.length) { truncated = true; break; }
    }
    return { text: parts.join('\n'), bytes, nodes, truncated };
  }

  function utf8Bytes(value) {
    const point = value.codePointAt(0);
    if (point <= 0x7f) return 1;
    if (point <= 0x7ff) return 2;
    if (point <= 0xffff) return 3;
    return 4;
  }

  function install(root, send, options) {
    const settings = Object.assign({}, options || {});
    if (!root || typeof send !== 'function' || typeof MutationObserver === 'undefined') return () => {};
    let stopped = false;
    let timer = 0;
    let lastSent = '';
    let lastRefresh = 0;
    let pending = false;
    const now = settings.now || (() => Date.now());
    const minInterval = Math.max(10000, settings.minIntervalMs || 10000);
    const debounce = Math.max(250, settings.debounceMs || 500);
    const emit = () => {
      if (stopped) return;
      const result = extractText(root, settings.limits);
      if (!result.text || result.text === lastSent) return;
      lastSent = result.text;
      lastRefresh = now();
      send({ version: 1, text: result.text, truncated: result.truncated, capturedAt: new Date(lastRefresh).toISOString() });
    };
    emit();
    const observer = new MutationObserver(() => {
      if (stopped || pending) return;
      pending = true;
      timer = setTimeout(() => {
        pending = false;
        const wait = minInterval - (now() - lastRefresh);
        if (wait > 0) { pending = true; timer = setTimeout(() => { pending = false; emit(); }, wait); }
        else emit();
      }, debounce);
    });
    observer.observe(root, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ['hidden', 'aria-hidden', 'style', 'class', 'contenteditable'] });
    return () => { stopped = true; lastSent = ''; observer.disconnect(); if (timer) clearTimeout(timer); };
  }

  function connectSafari() {
    if (typeof document === 'undefined' || typeof safari === 'undefined' || !safari.extension || !safari.self) return;
    let stopCapture = null;
    let lastQuery = 0;
    const requestState = (force) => {
      const now = Date.now();
      if (!force && now - lastQuery < 1000) return;
      lastQuery = now;
      safari.extension.dispatchMessage('blur-capture-state-query', {});
    };
    safari.self.addEventListener('message', (event) => {
      if (!event || event.name !== 'blur-capture-state') return;
      if (event.message && event.message.enabled === true && document.body) {
        if (!stopCapture) stopCapture = install(document.body, (snapshot) => safari.extension.dispatchMessage('blur-page-snapshot-v1', snapshot));
      } else if (stopCapture) {
        stopCapture();
        stopCapture = null;
      }
    });
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState !== 'visible') {
        if (stopCapture) { stopCapture(); stopCapture = null; }
      } else requestState(false);
    }, { passive: true });
    if (typeof window !== 'undefined') {
      window.addEventListener('focus', () => requestState(false), { passive: true });
      window.addEventListener('blur', () => { if (stopCapture) { stopCapture(); stopCapture = null; } }, { passive: true });
    }
    if (document.body) requestState(true);
    else document.addEventListener('DOMContentLoaded', () => requestState(true), { once: true, passive: true });
  }

  if (typeof document !== 'undefined') connectSafari();

  return { extractText, install, limits: DEFAULTS };
});
