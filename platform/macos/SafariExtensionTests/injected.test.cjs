const test = require('node:test');
const assert = require('node:assert/strict');
const { extractText } = require('../SafariExtension/Resources/Injected.js');

function tree(...children) {
  const root = { nodeType: 1, tagName: 'BODY', children, hidden: false, hasAttribute: () => false, getAttribute: () => null };
  const visit = (node) => { node.parentElement = node.parentElement || null; for (const child of node.children || []) { child.parentElement = node; visit(child); } };
  visit(root);
  root.ownerDocument = {
    defaultView: { getComputedStyle: () => ({ display: 'block', visibility: 'visible' }) },
    createTreeWalker(start, _what, filter) {
      const nodes = [];
      const walk = (node) => { if (filter.acceptNode(node) === 1) { nodes.push(node); for (const child of node.children || []) walk(child); } };
      for (const child of start.children || []) walk(child);
      let i = 0;
      return { nextNode: () => nodes[i++] || null };
    },
  };
  return root;
}
function element(tag, children = [], props = {}) { return { nodeType: 1, tagName: tag.toUpperCase(), children, hidden: false, hasAttribute: () => false, getAttribute: () => null, ...props }; }
function text(value) { return { nodeType: 3, nodeValue: value, children: [] }; }

test('extracts visible off-screen document text but excludes hidden and editable subtrees', () => {
  const root = tree(element('p', [text('Visible')]), element('div', [text('Offscreen')], { rect: { top: 9000 } }), element('div', [text('Secret')], { hidden: true }), element('textarea', [text('Draft')]), element('script', [text('code')]));
  const result = extractText(root, { maxNodes: 100, maxBytes: 1024, budgetMs: 100, now: (() => { let n = 0; return () => ++n; })() });
  assert.equal(result.text, 'Visible\nOffscreen');
  assert.equal(result.truncated, false);
});

test('stops at node and byte budgets and marks the snapshot truncated', () => {
  const root = tree(...Array.from({ length: 8 }, (_, i) => element('p', [text('row' + i)])));
  const byNodes = extractText(root, { maxNodes: 3, maxBytes: 1024, budgetMs: 100, now: (() => { let n=0; return () => ++n; })() });
  assert.equal(byNodes.truncated, true);
  const byBytes = extractText(root, { maxNodes: 100, maxBytes: 4, budgetMs: 100, now: (() => { let n=0; return () => ++n; })() });
  assert.ok(Buffer.byteLength(byBytes.text) <= 4);
  assert.equal(byBytes.truncated, true);
});

test('stops when extraction time budget is exhausted', () => {
  const root = tree(...Array.from({ length: 20 }, () => element('p', [text('row')])));
  let now = 0;
  const result = extractText(root, { maxNodes: 100, maxBytes: 1024, budgetMs: 1, now: () => now++ });
  assert.equal(result.truncated, true);
});

test('coalesces mutations, enforces the refresh floor, skips unchanged text, and disconnects', () => {
  const root = tree(element('p', [text('One')]));
  const oldObserver = global.MutationObserver;
  const oldSetTimeout = global.setTimeout;
  const oldClearTimeout = global.clearTimeout;
  const timers = new Map();
  let nextTimer = 1;
  let observer;
  let current = 1000;
  class FakeObserver {
    constructor(callback) { this.callback = callback; observer = this; }
    observe() {}
    disconnect() { this.disconnected = true; }
    trigger() { this.callback([]); }
  }
  global.MutationObserver = FakeObserver;
  global.setTimeout = (callback, delay) => { const id = nextTimer++; timers.set(id, { callback, delay }); return id; };
  global.clearTimeout = (id) => timers.delete(id);
  const runNext = () => { const [id, timer] = timers.entries().next().value; timers.delete(id); timer.callback(); return timer.delay; };
  const sent = [];
  try {
    const stop = require('../SafariExtension/Resources/Injected.js').install(root, (value) => sent.push(value.text), { now: () => current, debounceMs: 300, minIntervalMs: 10000 });
    assert.deepEqual(sent, ['One']);
    root.children[0].children[0].nodeValue = 'Two'; observer.trigger();
    assert.equal(timers.size, 1);
    assert.equal(timers.values().next().value.delay, 300);
    root.children[0].children[0].nodeValue = 'Latest'; observer.trigger();
    current = 11000;
    runNext();
    assert.deepEqual(sent, ['One', 'Latest']);
    observer.trigger(); current = 21000; runNext();
    assert.deepEqual(sent, ['One', 'Latest'], 'unchanged page text was sent again');
    stop();
    assert.equal(observer.disconnected, true);
    assert.equal(timers.size, 0);
  } finally {
    global.MutationObserver = oldObserver;
    global.setTimeout = oldSetTimeout;
    global.clearTimeout = oldClearTimeout;
  }
});
