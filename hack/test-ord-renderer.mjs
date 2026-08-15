import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(
  new URL(
    "../charts/chat-ui-ui/files/ui-extensions/ord/renderer.js",
    import.meta.url,
  ),
  "utf8",
);
const {
  configUrlFor,
  loadProviderDocuments,
  matchProviders,
  matchesVersion,
  parseProviderData,
} = await import(
  `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`
);

const current = {
  name: "chat-ui",
  providerMetadata: {
    spec: {
      displayName: "Chat UI",
      data: JSON.stringify({ ord: { configUrl: "https://chat.example/ord" } }),
    },
  },
};
const candidate = {
  name: "private-llm",
  providerMetadata: { spec: { displayName: "Private LLM" } },
};

assert.deepEqual(
  parseProviderData('{"ord":{"configUrl":"https://example.test"}}'),
  {
    ord: { configUrl: "https://example.test" },
  },
);
assert.equal(configUrlFor(current), "https://chat.example/ord");
assert.equal(matchesVersion("1.0.0", "1.0"), true);
assert.equal(matchesVersion("1.1.0", "1.0"), false);
assert.equal(matchesVersion("2.0.0", "1.9"), false);
assert.equal(matchesVersion("1.0.0", undefined), false);

await assert.rejects(
  loadProviderDocuments(current, async (url) =>
    url.endsWith("/ord")
      ? {
          openResourceDiscoveryV1: {
            documents: [
              { url: "./document.json", accessStrategies: [{ type: "open" }] },
            ],
          },
        }
      : { openResourceDiscovery: "1.17" },
  ),
  /Unsupported ORD version/,
);

const matches = matchProviders(
  current,
  [
    {
      integrationDependencies: [
        {
          title: "LLM backend",
          aspects: [
            {
              apiResources: [
                { ordId: "example:apiResource:chat:v1", minVersion: "1.0.0" },
              ],
            },
          ],
        },
      ],
    },
  ],
  [
    {
      provider: candidate,
      documents: [
        {
          apiResources: [
            {
              ordId: "example:apiResource:implementation:v1",
              title: "Chat API",
              visibility: "public",
              compatibleWith: [
                { ordId: "example:apiResource:chat:v1", maxVersion: "1.0" },
              ],
            },
            {
              ordId: "example:apiResource:name-only:v1",
              title: "example:apiResource:chat:v1",
              visibility: "public",
            },
          ],
        },
      ],
    },
  ],
);

assert.equal(matches.length, 1);
assert.deepEqual(
  matches[0].candidates.map(({ provider, api }) => [provider.name, api.ordId]),
  [["private-llm", "example:apiResource:implementation:v1"]],
);
