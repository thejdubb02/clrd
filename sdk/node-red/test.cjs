const assert = require('node:assert/strict');
const { EventEmitter, once } = require('node:events');
const { readFileSync } = require('node:fs');
const { createServer } = require('node:http');
const { test } = require('node:test');
const register = require('./clrd.js');

async function runtime(t, reply) {
    const requests = [];
    const server = createServer(async (req, res) => {
        let body = '';
        for await (const part of req) body += part;
        requests.push({ path: req.url, method: req.method, auth: req.headers.authorization, body: body && JSON.parse(body) });
        const { status = 200, json } = reply(requests.length - 1);
        res.writeHead(status, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(json));
    });
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    t.after(() => {
        server.closeAllConnections();
        return new Promise((done) => server.close(done));
    });

    const types = new Map();
    let credentials;
    let configNode;
    register({
        nodes: {
            createNode(node, config) {
                const emitter = new EventEmitter();
                node.on = emitter.on.bind(emitter);
                node.once = emitter.once.bind(emitter);
                node.removeListener = emitter.removeListener.bind(emitter);
                node.emit = emitter.emit.bind(emitter);
                if (config === serverConfig) node.credentials = credentials;
                node.status = () => {};
                node.send = (msg) => emitter.emit('message', msg);
                node.error = (error) => emitter.emit('error', error);
                node.warn = (warning) => emitter.emit('warning', warning);
            },
            getNode: () => configNode,
            registerType(name, Constructor, opts) {
                types.set(name, Constructor);
                if (name === 'clrd-server') {
                    assert.deepEqual(opts.credentials, { token: { type: 'password' } });
                    credentials = { token: 'test-token' };
                }
            },
        },
    });
    const serverConfig = { url: `http://127.0.0.1:${server.address().port}` };
    configNode = new (types.get('clrd-server'))(serverConfig);
    return {
        requests,
        configNode,
        node(type, config) {
            const node = new (types.get(type))(config);
            t.after(() => node.emit('close'));
            return node;
        },
    };
}

function input(node, msg) {
    return new Promise((resolve, reject) => {
        let output;
        node.emit('input', msg, (result) => { output = result; }, (error) => {
            if (error) reject(error);
            else resolve(output);
        });
    });
}

test('action maps every operation and keeps the rest of the message', { timeout: 5000 }, async (t) => {
    const app = await runtime(t, (i) => ({ json: { id: String(i) } }));
    const send = { message: 'Disk full', title: 'Alert', external_id: 'disk', priority: 4,
        icon: 'https://example.com/disk.png',
        attachment: { url: 'https://example.com/report.pdf', name: 'Report.pdf' },
        actions: [{ label: 'Copy ID', copy: 'disk' }], reopen: true };
    const values = [
        ['send', send],
        ['acknowledge', 'abc/123'],
        ['clear', { id: 'abc/123' }],
        ['resolve', 'abc/123'],
        ['snooze', { id: 'abc/123', duration: '15m' }],
    ];
    for (const [i, [operation, payload]] of values.entries()) {
        const node = app.node('clrd', { operation, topic: 'infra', duration: '1h' });
        assert.deepEqual(await input(node, { payload, marker: 'kept' }), { payload: { id: String(i) }, marker: 'kept' });
    }
    const idPath = '/api/v1/notifications/abc%2F123';
    assert.deepEqual(app.requests, [
        ['/api/v1/notifications', { topic: 'infra', ...send }],
        [idPath + '/ack', ''],
        [idPath + '/dismiss', ''],
        [idPath + '/resolve', ''],
        [idPath + '/snooze', { for: '15m' }],
    ].map(([path, body]) => ({ path, body, method: 'POST', auth: 'Bearer test-token' })));
});

test('action reports API failures through done', { timeout: 5000 }, async (t) => {
    const app = await runtime(t, () => ({ status: 403, json: { error: 'forbidden' } }));
    await assert.rejects(input(app.node('clrd', { operation: 'clear' }), { payload: 'id' }), /403: forbidden/);
});

test('event input skips history and stops polling on close', { timeout: 5000 }, async (t) => {
    assert.match(readFileSync(require.resolve('./clrd.html'), 'utf8'), /<option value="updated">Updated Notification<\/option>/);
    const notification = { id: 'a', topic: 'infra' };
    const event = { seq: 12, type: 'updated', notification };
    const pages = [
        { notifications: [{ id: 'old' }], seq: 10, epoch: 'db' },
        { events: [{ seq: 11, type: 'created', notification }, event], seq: 12, epoch: 'db' },
    ];
    const app = await runtime(t, (i) => ({ json: pages[i] }));
    const node = app.node('clrd-events', { event: 'updated', topic: 'infra', interval: 1 });
    assert.deepEqual(await once(node, 'message'), [{ payload: event, topic: 'infra' }]);
    node.emit('close');
    await new Promise((done) => setTimeout(done, 1100));
    assert.deepEqual(app.requests.map((request) => request.path), [
        '/api/v1/notifications?state=open&limit=1',
        '/api/v1/events?since=10&subscribed=all&topics=infra',
    ]);
});

test('action explains an invalid server URL', { timeout: 5000 }, async (t) => {
    const app = await runtime(t, () => assert.fail('Invalid URL must make no request'));
    app.configNode.url = 'clrd.example.com';
    await assert.rejects(input(app.node('clrd', { operation: 'clear' }), { payload: 'id' }),
        /Enter the clrd HTTP or HTTPS server URL/);
});

test('event polling backs off on repeated failures and recovers its normal interval', { timeout: 5000 }, async (t) => {
    const app = await runtime(t, () => assert.fail('Stubbed poll must make no HTTP request'));
    t.mock.timers.enable({ apis: ['setTimeout'] });
    let attempts = 0;
    app.configNode.connect = async () => {
        attempts++;
        if (attempts <= 2) throw new Error('Connection refused');
        return {
            async pollEvents() {
                return {
                    cursor: { seq: attempts, epoch: 'db' },
                    events: [{ type: 'created', notification: { topic: 'infra' } }],
                };
            },
        };
    };
    const node = app.node('clrd-events', { event: 'created', interval: 1 });
    await once(node, 'error');
    t.mock.timers.tick(1999);
    assert.equal(attempts, 1);
    const failed = once(node, 'error');
    t.mock.timers.tick(1);
    await failed;
    t.mock.timers.tick(3999);
    assert.equal(attempts, 2);
    const recovered = once(node, 'message');
    t.mock.timers.tick(1);
    await recovered;
    const regular = once(node, 'message');
    t.mock.timers.tick(1000);
    await regular;
    assert.equal(attempts, 4);
});
