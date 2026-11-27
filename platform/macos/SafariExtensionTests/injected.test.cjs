const test = require('node:test');
const assert = require('node:assert/strict');
const { extractText } = require('../SafariExtension/Resources/Injected.js');

function tree(...children) {
  const root = { nodeType: 1, tagName: 'BODY', children, hidden: false, hasAttribute: () => false, getAttribute: () => null };
  const visit = (node) => { node.parentElement = node.parentElement || null; for (const child of node.children || []) { child.parentElement = node; visit(child); } };
  visit(root);
  const order = [];
  const link = (node) => {
    order.push(node);
    Object.defineProperty(node, 'lastChild', { configurable: true, get: () => (node.children || []).at(-1) || null });
    node.contains = (other) => { for (let p = other; p; p = p.parentElement) if (p === node) return true; return false; };
    node.compareDocumentPosition = (other) => order.indexOf(other) > order.indexOf(node) ? 4 : 2;
    for (const child of node.children || []) link(child);
  };
  link(root);
  root.appendChild = (child) => { child.parentElement = root; root.children.push(child); visit(child); link(child); return child; };
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

function readDocument(root, limits) {
  const cursor = require('../SafariExtension/Resources/Injected.js').createCursor(root, { now: () => 0, ...limits });
  const parts = [];
  for (let i = 0; i < 2000; i++) {
    const batch = cursor.read();
    parts.push(batch.text);
    if (batch.done) return { text: parts.join(''), parts };
  }
  assert.fail('document cursor did not finish bounded continuation');
}

test('continues beyond byte and node work budgets through the final DOM text', () => {
  const long = 'Large page evidence. '.repeat(18000) + 'TAIL_ROLLBACK_MIRA';
  const root = tree(...Array.from({ length: 11000 }, (_, i) => element('p', [text('row ' + i)])), element('p', [text(long)]));
  const got = readDocument(root, { maxNodes: 10000, maxBytes: 32768, budgetMs: 100 });
  assert.ok(got.parts.length > 8);
  assert.ok(got.parts.every(part => Buffer.byteLength(part) <= 32768));
  assert.equal(got.text, Array.from({ length: 11000 }, (_, i) => 'row ' + i).join('\n') + '\n' + long);
});

test('ranges a huge single text node without whole-node reads and preserves Unicode boundaries', () => {
  const value = ('  Mira\t🙂\n').repeat(50000) + 'TAIL_OWNER';
  const node = text('');
  delete node.nodeValue;
  Object.defineProperty(node, 'nodeValue', { get: () => { assert.fail('read entire hostile text node'); } });
  Object.defineProperty(node, 'length', { get: () => value.length });
  node.substringData = (start, count) => { assert.ok(count <= 4097); return value.slice(start, start + count); };
  const got = readDocument(tree(node), { maxBytes: 32768, budgetMs: 100 });
  assert.equal(got.text, ('Mira 🙂 ').repeat(50000) + 'TAIL_OWNER');
});

function installHarness(root, sender, options = {}) {
  const old = { observer: global.MutationObserver, set: global.setTimeout, clear: global.clearTimeout };
  const timers = new Map();
  let next = 1;
  let observer;
  let current = 1000;
  global.MutationObserver = class {
    constructor(callback) { this.callback = callback; observer = this; }
    observe() {}
    takeRecords() { return []; }
    disconnect() {}
    trigger(records) { this.callback(records); }
  };
  global.setTimeout = (callback, delay) => { const id = next++; timers.set(id, { callback, delay }); return id; };
  global.clearTimeout = (id) => timers.delete(id);
  const stop = require('../SafariExtension/Resources/Injected.js').install(root, sender, { now: () => current, limits: { now: () => 0 }, ...options });
  return {
    observer: () => observer,
    runUntil(predicate) {
      for (let i = 0; !predicate() && i < 2000; i++) {
        assert.ok(timers.size, 'continuation was lost');
        const [id, timer] = timers.entries().next().value;
        timers.delete(id);
        current += timer.delay;
        timer.callback();
      }
      assert.ok(predicate(), 'continuation starved');
    },
    close() { stop(); global.MutationObserver = old.observer; global.setTimeout = old.set; global.clearTimeout = old.clear; },
  };
}

test('installation retries identical unacknowledged batches and captures changed tails', () => {
  const value = 'Stable prefix. '.repeat(25000) + 'TAIL_OLD';
  const tail = text(value);
  const sent = [];
  let reject = true;
  const harness = installHarness(tree(tail), frame => { sent.push(frame); if (reject) { reject = false; return false; } return true; });
  try {
    harness.runUntil(() => sent.some(frame => frame.complete));
    assert.deepEqual(sent[0], sent[1], 'failed batch advanced the DOM cursor');
    const unique = [...new Map(sent.map(frame => [frame.sequence, frame])).values()];
    assert.equal(unique.map(frame => frame.text).join(''), value);
    assert.equal(unique.at(-1).structureLimited, false);
    const revision = sent[0].revision;
    tail.nodeValue = value.replace('TAIL_OLD', 'TAIL_NEW');
    harness.observer().trigger([{ type: 'characterData', target: tail }]);
    harness.runUntil(() => sent.some(frame => frame.revision !== revision && frame.complete));
    assert.equal(sent.filter(frame => frame.revision !== revision).map(frame => frame.text).join(''), tail.nodeValue);
  } finally { harness.close(); }
});

test('continuous appends beyond the initial DOM fence cannot starve a stable complete revision', () => {
  const value = 'Initial page. '.repeat(25000) + 'INITIAL_TAIL';
  const root = tree(text(value));
  const sent = [];
  const harness = installHarness(root, frame => { sent.push(frame); return true; });
  try {
    const original = sent[0].revision;
    for (let i = 0; !sent.some(frame => frame.complete) && i < 100; i++) {
      const node = root.appendChild(text('Appended ' + i));
      harness.observer().trigger([{ type: 'childList', target: root, addedNodes: [node], removedNodes: [] }]);
      const before = sent.length;
      harness.runUntil(() => sent.length > before);
    }
    const first = sent.filter(frame => frame.revision === original);
    assert.equal(first.at(-1).complete, true);
    assert.equal(first.at(-1).structureLimited, false);
    assert.equal(first.map(frame => frame.text).join(''), value);
  } finally { harness.close(); }
});

test('edits inside an unfinished revision publish only its stable prefix with partial coverage', () => {
  const value = 'Old body. '.repeat(30000) + 'OLD_TAIL';
  const node = text(value);
  const sent = [];
  const harness = installHarness(tree(node), frame => { sent.push(frame); return true; });
  try {
    const original = sent[0].revision;
    node.nodeValue = 'Entire replacement. '.repeat(30000) + 'NEW_TAIL';
    harness.observer().trigger([{ type: 'characterData', target: node }]);
    harness.runUntil(() => sent.some(frame => frame.revision === original && frame.complete));
    const first = sent.filter(frame => frame.revision === original);
    assert.equal(first.at(-1).structureLimited, true);
    assert.ok(value.startsWith(first.map(frame => frame.text).join('')));
    assert.ok(!first.some(frame => frame.text.includes('replacement')));
  } finally { harness.close(); }
});

test('fragment navigation keeps the same finite document revision', () => {
  const value = 'Stable document. '.repeat(25000) + 'FINAL_TAIL';
  const root = tree(text(value));
  root.ownerDocument.location = { href: 'https://example.test/document#first' };
  const sent = [];
  const harness = installHarness(root, frame => { sent.push(frame); return true; });
  try {
    root.ownerDocument.location.href = 'https://example.test/document#second';
    harness.runUntil(() => sent.some(frame => frame.complete));
    assert.equal(new Set(sent.map(frame => frame.revision)).size, 1);
    assert.equal(sent.map(frame => frame.text).join(''), value);
    assert.ok(sent.every(frame => frame.documentURL === 'https://example.test/document'));
  } finally { harness.close(); }
});

test('replaced page bodies stop the old cursor and request fresh authority', () => {
  const root = tree(text('Detached old document. '.repeat(20000)));
  root.ownerDocument.body = root;
  const sent = [];
  let replaced = 0;
  const harness = installHarness(root, frame => { sent.push(frame); return true; }, { onRootChanged: () => replaced++ });
  try {
    root.ownerDocument.body = tree(text('Replacement body'));
    harness.runUntil(() => replaced > 0);
    assert.equal(sent.length, 1, 'continued sending a detached body');
  } finally { harness.close(); }
});

test('subframes never request native top-level page capture', () => {
  const vm = require('node:vm');
  const source = require('node:fs').readFileSync(require.resolve('../SafariExtension/Resources/Injected.js'), 'utf8');
  const messages = [];
  const context = {
    document: { body: {}, addEventListener() {} },
    window: { top: {}, addEventListener() {} },
    safari: { extension: { dispatchMessage: (...args) => messages.push(args) }, self: { addEventListener() {} } },
  };
  vm.runInNewContext(source, context);
  assert.deepEqual(messages, []);
});

test('body replacement rechecks native authority even inside the query refresh floor', () => {
  const vm = require('node:vm');
  const source = require('node:fs').readFileSync(require.resolve('../SafariExtension/Resources/Injected.js'), 'utf8');
  const root = tree(text('Original body with useful visible evidence.'));
  const document = root.ownerDocument;
  Object.assign(document, { body: root, documentElement: {}, visibilityState: 'visible', addEventListener() {} });
  const messages = [], observers = [];
  let messageReceived;
  const window = { addEventListener() {} }; window.top = window;
  const context = {
    document, window, Date: { now: () => 1000 }, setTimeout: () => 1, clearTimeout() {},
    MutationObserver: class {
      constructor(callback) { this.callback = callback; observers.push(this); }
      observe() {} disconnect() {} takeRecords() { return []; }
    },
    safari: { extension: { dispatchMessage: (...args) => messages.push(args) }, self: { addEventListener: (_name, callback) => { messageReceived = callback; } } },
  };
  vm.runInNewContext(source, context);
  messageReceived({ name: 'blur-capture-state', message: { enabled: true } });
  document.body = tree(text('New body with fresh visible evidence.'));
  document.body.ownerDocument = document;
  observers[0].callback();
  assert.equal(messages.filter(([name]) => name === 'blur-capture-state-query').length, 2);
});

test('extracts visible off-screen document text but excludes hidden and editable subtrees', () => {
  const root = tree(element('p', [text('Visible')]), element('div', [text('Offscreen')], { rect: { top: 9000 } }), element('div', [text('Secret')], { hidden: true }), element('textarea', [text('Draft')]), element('script', [text('code')]));
  const result = extractText(root, { maxNodes: 100, maxBytes: 1024, budgetMs: 100, now: (() => { let n = 0; return () => ++n; })() });
  assert.equal(result.text, 'Visible\nOffscreen');
  assert.equal(result.truncated, false);
});

test('never reads text inside an excluded root and identifies opaque embedded content', () => {
  const secret = text('');
  Object.defineProperty(secret, 'nodeValue', { get: () => { assert.fail('read excluded root text'); } });
  const hidden = tree(secret);
  hidden.hidden = true;
  assert.equal(extractText(hidden).text, '');
  assert.equal(extractText(tree(text('Visible'), element('canvas'))).structureLimited, true);
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
    assert.ok(timers.values().next().value.delay >= 300);
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
