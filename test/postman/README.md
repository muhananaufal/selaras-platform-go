# Postman collection

`selaras.postman_collection.json` is the whole edge.v1 flow over the Connect
protocol, runnable with no setup: `baseUrl` already points at the local stack,
the email is unique per run, the token and every slug/id are captured from the
previous answer, and every request asserts its status.

```bash
task up:full        # the stack must be running
task postman:run    # Newman runs the same collection from the CLI
```

In Postman: Import → pick this file → Run collection. The folder order is the
flow order (Auth → Profile → Assessment → Dashboard → Coaching → Chat →
Culinary → Account); do not shuffle it - Coaching needs the assessment slug,
and Account deletes its account at the end.

Every request is `POST {{baseUrl}}/<package>.<Service>/<Method>` with a JSON
body and `Connect-Protocol-Version: 1`. The `Watch*` server streams are not in
the collection - Postman cannot read the Connect streaming envelope; the e2e
suite covers them.

The collection is BUILT, not written by hand: `node build.js <output>`. It
holds no saved responses on purpose. The source of truth for the contract is
`api/proto/edge/v1`.
