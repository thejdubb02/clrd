const sdk = () => import('./dist/client.mjs');

module.exports = function register(RED) {
    function ClrdServer(config) {
        RED.nodes.createNode(this, config);
        this.url = config.url;

        this.connect = async (signal) => {
            const url = new URL(this.url.trim());
            const token = this.credentials?.token?.trim();
            if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
                throw new Error('Enter the clrd HTTP or HTTPS server URL without credentials, query or fragment');
            }
            if (!token) throw new Error('Enter a clrd API token');

            const { Client } = await sdk();
            return new Client({
                url: url.href,
                token,
                fetch: (input, init) => fetch(input, {
                    ...init,
                    redirect: 'error',
                    signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
                }),
            });
        };
    }

    function ClrdAction(config) {
        RED.nodes.createNode(this, config);
        const server = RED.nodes.getNode(config.server);
        const controller = new AbortController();
        this.on('close', () => controller.abort());
        this.on('input', async (msg, send, done) => {
            try {
                if (!server) throw new Error('Configure a clrd server first');
                const client = await server.connect(controller.signal);
                const payload = msg.payload;
                if (config.operation === 'send') {
                    const fields = typeof payload === 'string' ? { message: payload } : payload;
                    const { topic = config.topic, message, title, external_id: id, priority } = fields ?? {};
                    if (!topic || typeof message !== 'string') {
                        throw new Error('Send needs a topic and a message string');
                    }
                    msg.payload = await client.send(topic, message, { title, id, priority });
                } else {
                    const id = typeof payload === 'string' ? payload : payload?.id;
                    if (typeof id !== 'string' || !id.trim()) throw new Error('Enter a notification ID in msg.payload');
                    const actions = {
                        acknowledge: () => client.ack(id),
                        clear: () => client.dismiss(id),
                        resolve: () => client.resolve(id),
                        snooze: () => client.snooze(id, payload?.duration ?? config.duration),
                    };
                    if (!Object.hasOwn(actions, config.operation)) throw new Error('Select a clrd operation');
                    msg.payload = await actions[config.operation]();
                }
                if (!controller.signal.aborted) send(msg);
                done();
            } catch (error) {
                done(controller.signal.aborted ? undefined : error);
            }
        });
    }

    function ClrdEvents(config) {
        RED.nodes.createNode(this, config);
        const server = RED.nodes.getNode(config.server);
        const controller = new AbortController();
        const seconds = Number(config.interval);
        const delay = Number.isInteger(seconds) && seconds >= 1 && seconds <= 3600 ? seconds * 1000 : 5000;
        let timer;
        let cursor;

        this.on('close', () => {
            controller.abort();
            clearTimeout(timer);
        });
        if (!server) {
            this.error('Configure a clrd server first');
            return;
        }
        const poll = async () => {
            try {
                const client = await server.connect(controller.signal);
                const page = await client.pollEvents(cursor, { topic: config.topic });
                if (controller.signal.aborted) return;
                cursor = page.cursor;
                this.status({ fill: 'green', shape: 'dot', text: 'polling' });
                if (page.reset) this.warn('clrd event history reset; resuming from the current head');
                for (const event of page.events) {
                    if (event.type === config.event && event.notification) {
                        this.send({ payload: event, topic: event.notification.topic });
                    }
                }
            } catch (error) {
                if (controller.signal.aborted) return;
                this.status({ fill: 'red', shape: 'ring', text: 'poll failed' });
                this.error(error);
            }
            if (!controller.signal.aborted) timer = setTimeout(poll, delay);
        };
        void poll();
    }

    RED.nodes.registerType('clrd-server', ClrdServer, { credentials: { token: { type: 'password' } } });
    RED.nodes.registerType('clrd', ClrdAction);
    RED.nodes.registerType('clrd-events', ClrdEvents);
};
