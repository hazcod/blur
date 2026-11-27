(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.BlurPageCapture = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const DEFAULTS = Object.freeze({ maxNodes: 10000, maxBytes: 256 * 1024, budgetMs: 100 });
  const EXCLUDED = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'TEXTAREA', 'INPUT', 'SELECT', 'OPTION', 'IFRAME', 'OBJECT', 'EMBED']);
  const OPAQUE = new Set(['CANVAS', 'IFRAME', 'OBJECT', 'EMBED']);

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

  // A round ceiling is a yield point, never an end-of-document claim.
  function createCursor(root, limits) {
    const options = Object.assign({}, DEFAULTS, limits || {});
    const document = root && root.ownerDocument;
    const now = options.now || (() => (typeof performance !== 'undefined' ? performance.now() : Date.now()));
    const valid = root && document && document.createTreeWalker && options.maxNodes > 0 && options.maxBytes >= 4 && options.budgetMs > 0;
    const walker = valid ? document.createTreeWalker(root, 0xffffffff, { acceptNode: () => 1 }) : null;
    const hidden = new WeakMap();
    let fence = root;
    let fenceReady = false;
    let done = !valid;
    let limited = false;
    let current = null;
    let offset = 0;
    let length = 0;
    let buffer = '';
    let bufferOffset = 0;
    let nodeText = false;
    let pendingSpace = false;
    let anyText = false;
    let rootChecked = false;

    const cursor = {
      invalidate() { limited = true; done = true; current = null; buffer = ''; },
      afterFence(node) {
        return fenceReady && fence && typeof fence.compareDocumentPosition === 'function' &&
          (fence.compareDocumentPosition(node) & 5) === 4;
      },
      read() {
        const started = now();
        let nodes = 0;
        let bytes = 0;
        const parts = [];
        while (!done) {
          if (now() - started >= options.budgetMs || nodes >= options.maxNodes) break;
          if (!rootChecked) { rootChecked = true; hidden.set(root, excluded(root, document, hidden)); }
          if (!fenceReady) {
            // Resolve even a deeply nested final leaf in bounded rounds.
            if (fence && fence.lastChild) { fence = fence.lastChild; nodes++; continue; }
            fenceReady = true;
          }
          if (!current) {
            const node = walker.nextNode();
            if (!node) { done = true; break; }
            nodes++;
            const parentBlocked = node.parentElement && hidden.get(node.parentElement) === true;
            if (node.nodeType === 1) {
              if (!parentBlocked && OPAQUE.has(node.tagName)) limited = true;
              hidden.set(node, parentBlocked || excluded(node, document, hidden));
              if (node === fence) done = true;
              continue;
            }
            if (node.nodeType !== 3 || parentBlocked) {
              if (node === fence) done = true;
              continue;
            }
            current = node;
            offset = 0;
            length = typeof node.length === 'number' ? node.length : String(node.nodeValue || '').length;
            nodeText = false;
            pendingSpace = false;
            buffer = '';
            bufferOffset = 0;
          }
          if (offset >= length) {
            if (current === fence) done = true;
            current = null;
            buffer = '';
            continue;
          }
          if (!buffer || bufferOffset >= buffer.length) {
            const count = Math.min(4096, length - offset);
            buffer = typeof current.substringData === 'function' ? current.substringData(offset, Math.min(count + 1, length - offset)) : String(current.nodeValue || '').slice(offset, offset + count + 1);
            const last = buffer.charCodeAt(buffer.length - 1);
            if (last >= 0xd800 && last <= 0xdbff && offset + buffer.length < length) buffer = buffer.slice(0, -1);
            bufferOffset = 0;
            if (!buffer) { cursor.invalidate(); break; }
          }
          const point = buffer.codePointAt(bufferOffset);
          let character = String.fromCodePoint(point);
          if (point === 0 || point >= 0xd800 && point <= 0xdfff) { character = ' '; limited = true; }
          const consumed = point > 0xffff ? 2 : 1;
          if (/\s/u.test(character)) {
            if (nodeText) pendingSpace = true;
            offset += consumed;
            bufferOffset += consumed;
            continue;
          }
          const prefix = nodeText ? (pendingSpace ? ' ' : '') : (anyText ? '\n' : '');
          const width = prefix.length + utf8Bytes(character);
          if (bytes + width > options.maxBytes) break;
          parts.push(prefix, character);
          bytes += width;
          offset += consumed;
          bufferOffset += consumed;
          nodeText = true;
          anyText = true;
          pendingSpace = false;
        }
        return { text: parts.join(''), bytes, nodes, done, truncated: !done, structureLimited: limited };
      },
    };
    return cursor;
  }

  function extractText(root, limits) { return createCursor(root, limits).read(); }

  function utf8Bytes(value) {
    const point = value.codePointAt(0);
    if (point <= 0x7f) return 1;
    if (point <= 0x7ff) return 2;
    if (point <= 0xffff) return 3;
    return 4;
  }

  let nextRevision = 0;
  const documentID = (() => {
    const bytes = new Uint8Array(16);
    if (typeof crypto !== 'undefined' && crypto.getRandomValues) crypto.getRandomValues(bytes);
    else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
    return Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('');
  })();

  function install(root, send, options) {
    const settings = Object.assign({}, options || {});
    if (!root || typeof send !== 'function' || typeof MutationObserver === 'undefined') return () => {};
    const now = settings.now || (() => Date.now());
    const minInterval = Math.max(10000, settings.minIntervalMs || 10000);
    const debounce = Math.max(250, settings.debounceMs || 500);
    const limits = Object.assign({}, settings.limits || {}, { maxBytes: Math.min(32768, settings.limits && settings.limits.maxBytes || DEFAULTS.maxBytes) });
    let stopped = false;
    let timer = 0;
    let scan = null;
    let dirty = false;
    let lastStart = -minInterval;
    let lastSingleText = '';
    let lastSingleLimited = false;
    const documentURL = () => root.ownerDocument && root.ownerDocument.location ? root.ownerDocument.location.href.split('#')[0] : '';
    const schedule = (delay) => {
      if (stopped || timer) return;
      timer = setTimeout(() => { timer = 0; pump(); }, Math.max(0, delay));
    };
    const changed = (records) => {
      if (stopped) return;
      dirty = true;
      if (scan) {
        const outside = records.length > 0 && records.every(record => {
          if (record.type === 'childList') return record.removedNodes.length === 0 && record.addedNodes.length > 0 && Array.from(record.addedNodes).every(node => scan.cursor.afterFence(node));
          return scan.cursor.afterFence(record.target);
        });
        if (!outside && !(scan.pending && scan.pending.complete)) scan.cursor.invalidate();
        if (!scan.pending) schedule(0);
      } else schedule(Math.max(debounce, minInterval - (now() - lastStart)));
    };
    const observer = new MutationObserver(changed);
    observer.observe(root, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ['hidden', 'aria-hidden', 'style', 'class', 'contenteditable', 'inert'] });

    function pump() {
      if (stopped) return;
      if (root.ownerDocument && 'body' in root.ownerDocument && root.ownerDocument.body !== root) {
        stop();
        if (typeof settings.onRootChanged === 'function') settings.onRootChanged();
        return;
      }
      const records = typeof observer.takeRecords === 'function' ? observer.takeRecords() : [];
      if (records.length) changed(records);
      if (!scan) {
        const wait = minInterval - (now() - lastStart);
        if (wait > 0) { schedule(wait); return; }
        dirty = false;
        lastStart = now();
        scan = { cursor: createCursor(root, limits), revision: ++nextRevision, sequence: 0, documentURL: documentURL(), pending: null };
      }
      const owner = scan;
      if (owner.documentURL !== documentURL()) { scan = null; dirty = true; schedule(minInterval); return; }
      if (!owner.pending) {
        const result = owner.cursor.read();
        if (result.done && owner.sequence === 0 && (!result.text || result.text === lastSingleText && result.structureLimited === lastSingleLimited)) { scan = null; return; }
        owner.pending = { version: 2, documentID, documentURL: owner.documentURL, revision: owner.revision, sequence: owner.sequence, text: result.text, complete: result.done, structureLimited: result.structureLimited };
      }
      const frame = owner.pending;
      const acknowledged = (reply) => {
        if (stopped || scan !== owner) return;
        if (reply === false || reply && reply.ok === false) {
          if (reply && reply.restart === true) { scan = null; dirty = true; schedule(minInterval); }
          else schedule(1000);
          return;
        }
        owner.pending = null;
        owner.sequence++;
        if (frame.complete) {
          lastSingleText = frame.sequence === 0 ? frame.text : '';
          lastSingleLimited = frame.structureLimited;
          scan = null;
          if (dirty) schedule(Math.max(debounce, minInterval - (now() - lastStart)));
        } else schedule(0);
      };
      try {
        const reply = send(frame);
        if (reply && typeof reply.then === 'function') reply.then(acknowledged, () => acknowledged(false));
        else acknowledged(reply);
      } catch (_) { acknowledged(false); }
    }
    function stop() { stopped = true; scan = null; lastSingleText = ''; observer.disconnect(); if (timer) clearTimeout(timer); timer = 0; }
    pump();
    return stop;
  }

  function connectSafari() {
    if (typeof document === 'undefined' || typeof safari === 'undefined' || !safari.extension || !safari.self) return;
    if (typeof window !== 'undefined' && window.top !== window) return;
    let stopCapture = null;
    let capturedRoot = null;
    let lastQuery = 0;
    const waiting = new Map();
    const send = snapshot => new Promise(resolve => {
      const key = snapshot.revision + ':' + snapshot.sequence;
      const timeout = setTimeout(() => { waiting.delete(key); resolve(false); }, 3000);
      waiting.set(key, reply => { clearTimeout(timeout); waiting.delete(key); resolve(reply); });
      safari.extension.dispatchMessage('blur-page-snapshot-v2', snapshot);
    });
    const requestState = (force) => {
      const now = Date.now();
      if (!force && now - lastQuery < 1000) return;
      lastQuery = now;
      safari.extension.dispatchMessage('blur-capture-state-query', {});
    };
    safari.self.addEventListener('message', (event) => {
      if (!event) return;
      if (event.name === 'blur-page-capture-ack') {
        const reply = event.message || {};
        const complete = waiting.get(reply.revision + ':' + reply.sequence);
        if (complete) complete(reply);
        return;
      }
      if (event.name !== 'blur-capture-state') return;
      if (event.message && event.message.enabled === true && document.body) {
        if (!stopCapture) {
          capturedRoot = document.body;
          stopCapture = install(capturedRoot, send, { onRootChanged: () => { stopCapture = null; capturedRoot = null; requestState(true); } });
        }
      } else if (stopCapture) {
        stopCapture();
        stopCapture = null;
      }
    });
    // Body replacement is outside a body-scoped observer. Re-authorize the new
    // root even when the old body had already finished its finite snapshot.
    if (typeof MutationObserver !== 'undefined' && document.documentElement) {
      let body = document.body;
      const roots = new MutationObserver(() => {
        if (document.body === body) return;
        body = document.body;
        if (stopCapture && document.body !== capturedRoot) { stopCapture(); stopCapture = null; capturedRoot = null; }
        if (!stopCapture && document.body && document.visibilityState === 'visible') requestState(true);
      });
      roots.observe(document.documentElement, { childList: true });
    }
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

  return { createCursor, extractText, install, limits: DEFAULTS };
});
