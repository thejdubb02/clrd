# clrd for JavaScript

No dependencies. ESM, types included. Node 18+, Deno, Bun, browsers (anything with `fetch`).
Published as `clrd-client` (the `clrd` name on npm belongs to an unrelated project).

```js
import { send, resolve, Client } from "clrd-client";

// CLRD_URL and CLRD_TOKEN from the environment
await send("infra", "Disk 91%", { id: "disk-build", priority: 4 });
await resolve("infra", "disk-build"); // clears it on every device

const c = new Client({ url: "https://clrd.itsjustin.me", token: "clrd_..." });
const n = await c.send("deploys", "Ship v2?");
await c.ack(n.id);
await c.snooze(n.id, "1h");
await c.list({ topic: "infra" });
```

Errors throw `ClrdError` with `.status` and `.code`.

Tests: `npm test`
