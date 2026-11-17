import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(
  new URL(
    "../charts/chat-ui-ui/files/ui-extensions/login-secrets/login-secrets.js",
    import.meta.url,
  ),
  "utf8",
);
const { QUERY, gatewayFor, load, rowsFrom } = await import(
  `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`
);

const portal = "https://org.portal.example";
const context = {
  token: "portal-token",
  kcpPath: "root:orgs:org:demo",
  portalContext: {
    crdGatewayApiUrl: `${portal}/gateway/api/clusters/root:orgs:org/graphql`,
  },
};

// The request asks for labelled login Secrets only.
assert.match(
  QUERY,
  /Secrets\(labelselector: "ui\.privatellms\.msp\/login-secret=true"\)/,
);

// The endpoint follows the current account and stays on the portal's origin.
assert.equal(
  gatewayFor(context, portal),
  `${portal}/gateway/api/clusters/root:orgs:org:demo/graphql`,
);
assert.equal(gatewayFor(context, "https://other.example"), undefined);
assert.equal(gatewayFor({}, portal), undefined);
assert.equal(
  gatewayFor({ ...context, kcpPath: "../other" }, portal),
  `${portal}/gateway/api/clusters/root:orgs:org/graphql`,
);

const encode = (value) => Buffer.from(value).toString("base64");
const data = {
  ui_privatellms_msp: {
    v1alpha1: {
      ChatUIInstances: {
        items: [
          {
            metadata: { name: "zeta", namespace: "default" },
            status: {
              url: "https://zeta.chat.example",
              loginSecretName: "chatui-login-zeta-1",
            },
          },
          {
            metadata: { name: "alpha", namespace: "team" },
            status: {
              url: "javascript:alert(1)",
              loginSecretName: "chatui-login-2",
            },
          },
          // Organization login: no login Secret.
          { metadata: { name: "mapped", namespace: "default" }, status: {} },
        ],
      },
    },
  },
  v1: {
    Secrets: {
      items: [
        {
          metadata: { name: "chatui-login-zeta-1", namespace: "default" },
          data: { password: encode("zeta-password") },
        },
        {
          metadata: { name: "chatui-login-2", namespace: "team" },
          data: { password: encode("alpha-password") },
        },
        // Same Secret name in another namespace belongs to no instance there.
        {
          metadata: { name: "chatui-login-2", namespace: "default" },
          data: {},
        },
      ],
    },
  },
};

assert.deepEqual(rowsFrom(data), [
  {
    instance: "alpha",
    url: undefined,
    namespace: "team",
    secret: "chatui-login-2",
    password: "alpha-password",
  },
  {
    instance: "zeta",
    url: "https://zeta.chat.example/",
    namespace: "default",
    secret: "chatui-login-zeta-1",
    password: "zeta-password",
  },
  {
    instance: undefined,
    url: undefined,
    namespace: "default",
    secret: "chatui-login-2",
    password: undefined,
  },
]);
assert.deepEqual(rowsFrom({ v1: { Secrets: { items: [] } } }), []);

// The token is sent as a bearer header to the account endpoint, without cookies.
const calls = [];
const respond = (status, body) => async (url, options) => {
  calls.push({ url, options });
  return { ok: status === 200, status, json: async () => body };
};
const rows = await load(context, portal, respond(200, { data }));
assert.equal(rows.length, 3);
assert.equal(calls.length, 1);
assert.equal(
  calls[0].url,
  `${portal}/gateway/api/clusters/root:orgs:org:demo/graphql`,
);
assert.equal(calls[0].options.method, "POST");
assert.equal(calls[0].options.credentials, "omit");
assert.equal(calls[0].options.headers.Authorization, "Bearer portal-token");
assert.equal(JSON.parse(calls[0].options.body).query, QUERY);

// No token leaves for another origin, and failures are not shown as an empty list.
await assert.rejects(load(context, "https://other.example", respond(200, {})));
await assert.rejects(load({ ...context, token: "" }, portal, respond(200, {})));
assert.equal(calls.length, 1);
await assert.rejects(load(context, portal, respond(401, {})), /status 401/);
await assert.rejects(
  load(context, portal, respond(200, { errors: [{ message: "forbidden" }] })),
  /no Secrets/,
);
// Unreadable instances still leave the labelled Secrets visible.
assert.equal(
  (
    await load(
      context,
      portal,
      respond(200, {
        data: { v1: data.v1 },
        errors: [{ message: "forbidden" }],
      }),
    )
  ).every((row) => row.instance === undefined),
  true,
);

console.log("login secrets page: ok");
