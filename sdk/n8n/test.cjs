const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');

function nodeClass(path) {
  assert.ok(readFileSync(require.resolve(`./dist/${path}`)).length);
  return Object.values(require(`./dist/${path}`))[0];
}

const Clrd = nodeClass('nodes/Clrd.node.js');
const ClrdTrigger = nodeClass('nodes/ClrdTrigger.node.js');

function context(items, reply) {
  const requests = [];
  const staticData = {};
  return {
    requests,
    staticData,
    getInputData: () => items,
    getNodeParameter: (name, index = 0) => items[index][name],
    getCredentials: async () => ({ url: 'https://clrd.example.com/clrd/', token: 'test-token' }),
    getMode: () => 'trigger',
    getWorkflowStaticData: () => staticData,
    getNode: () => ({ name: 'clrd' }),
    continueOnFail: () => false,
    helpers: {
      async httpRequest(options) {
        assert.equal(options.headers.Authorization, 'Bearer test-token');
        assert.equal(options.disableFollowRedirect, true);
        requests.push([
          options.method,
          options.url.replace('https://clrd.example.com/clrd', ''),
          options.body ? JSON.parse(options.body) : undefined,
        ]);
        return reply(requests.length - 1);
      },
    },
  };
}

test('action maps each operation and keeps the output paired to its input', async () => {
  const input = [
    { operation: 'send', topic: 'infra', message: 'Disk full', additionalFields: { id: 'disk', title: 'Alert' } },
    ...['acknowledge', 'clear', 'resolve', 'snooze'].map((operation) => ({
      operation, notificationId: '01/example', duration: '15m',
    })),
  ];
  const ctx = context(input, (i) => ({ statusCode: 200, body: JSON.stringify({ id: String(i) }) }));
  const [items] = await new Clrd().execute.call(ctx);
  assert.deepEqual(items, input.map((_, i) => ({ json: { id: String(i) }, pairedItem: i })));
  const idPath = '/api/v1/notifications/01%2Fexample';
  assert.deepEqual(ctx.requests, [
    ['POST', '/api/v1/notifications', { topic: 'infra', message: 'Disk full', external_id: 'disk', title: 'Alert' }],
    ['POST', idPath + '/ack', undefined],
    ['POST', idPath + '/dismiss', undefined],
    ['POST', idPath + '/resolve', undefined],
    ['POST', idPath + '/snooze', { for: '15m' }],
  ]);
});

test('action surfaces API errors and can continue to the next item', async () => {
  const ctx = context(
    ['gone', 'current'].map((notificationId) => ({ operation: 'clear', notificationId })),
    (i) => i ? { statusCode: 200, body: { id: 'current' } } : { statusCode: 403, body: { error: 'forbidden' } },
  );
  ctx.continueOnFail = () => true;
  assert.deepEqual(await new Clrd().execute.call(ctx), [[
    { json: { error: '403: forbidden' }, pairedItem: 0 },
    { json: { id: 'current' }, pairedItem: 1 },
  ]]);
});

test('trigger skips old history, filters events, and advances its cursor', async () => {
  const notification = { id: 'a', topic: 'infra' };
  const event = { seq: 13, type: 'created', notification };
  const pages = [
    { notifications: [{ id: 'old' }], seq: 10, epoch: 'db' },
    { events: [], seq: 12, epoch: 'db' },
    { events: [event, { seq: 14, type: 'state', notification }], seq: 14, epoch: 'db' },
  ];
  const ctx = context([{ topic: 'infra', event: 'created' }], (i) => ({ statusCode: 200, body: pages[i] }));
  const poll = new ClrdTrigger().poll.bind(ctx);
  assert.equal(await poll(), null);
  assert.equal(await poll(), null);
  assert.deepEqual(await poll(), [[{ json: event }]]);
  assert.deepEqual(ctx.staticData.cursor, { seq: 14, epoch: 'db' });
  assert.deepEqual(ctx.requests, [
    ['GET', '/api/v1/notifications?state=open&limit=1', undefined],
    ['GET', '/api/v1/events?since=10&subscribed=all&topics=infra', undefined],
    ['GET', '/api/v1/events?since=12&subscribed=all&topics=infra', undefined],
  ]);
});

test('manual sample leaves the live cursor untouched', async () => {
  const notification = { id: 'a', topic: 'infra', seq: 2 };
  const ctx = context([{ topic: 'infra', event: 'state' }], () => ({
    statusCode: 200, body: { notifications: [notification] },
  }));
  ctx.getMode = () => 'manual';
  assert.deepEqual(await new ClrdTrigger().poll.call(ctx), [[{ json: { seq: 2, type: 'sample', notification } }]]);
  assert.deepEqual(ctx.staticData, {});
});
