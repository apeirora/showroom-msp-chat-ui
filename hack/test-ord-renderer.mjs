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
  groupCandidatesByProvider,
  loadProviderDocuments,
  matchProviders,
  matchesVersion,
  parseProviderData,
  providerIconUrl,
  resourceDefinitionUrlFor,
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
const providerWithIcon = {
  providerMetadata: {
    spec: {
      icon: {
        light: { data: "data:image/png;base64,aGVsbG8=" },
        dark: { url: "https://example.test/dark.svg" },
      },
    },
  },
};
assert.equal(
  providerIconUrl(providerWithIcon),
  "data:image/png;base64,aGVsbG8=",
);
assert.equal(
  providerIconUrl(providerWithIcon, true),
  "https://example.test/dark.svg",
);
assert.equal(
  providerIconUrl({
    providerMetadata: { spec: { icon: { light: { data: "javascript:x" } } } },
  }),
  undefined,
);
assert.equal(
  providerIconUrl({
    providerMetadata: {
      spec: {
        icon: {
          light: {
            url: "http://example.test/icon.svg",
            data: "data:image/png;base64,aGVsbG8=",
          },
        },
      },
    },
  }),
  "data:image/png;base64,aGVsbG8=",
);
assert.equal(matchesVersion("1.0.0", "1.0"), true);
assert.equal(
  resourceDefinitionUrlFor(
    {
      resourceDefinitions: [
        {
          url: "../definitions/chat.oas3.json",
          accessStrategies: [{ type: "open" }],
        },
      ],
    },
    "https://example.test/ord/documents/chat.json",
  ),
  "https://example.test/ord/definitions/chat.oas3.json",
);
assert.equal(
  resourceDefinitionUrlFor(
    {
      resourceDefinitions: [
        {
          url: "javascript:alert(1)",
          accessStrategies: [{ type: "open" }],
        },
      ],
    },
    "https://example.test/ord/document.json",
  ),
  undefined,
);
assert.equal(
  resourceDefinitionUrlFor(
    {
      resourceDefinitions: [
        {
          url: "/private/openapi.json",
          accessStrategies: [{ type: "oauth2" }],
        },
      ],
    },
    "https://example.test/ord/document.json",
  ),
  undefined,
);
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
      apiResources: [
        {
          ordId: "example:apiResource:chat:v1",
          title: "OpenAI-compatible Chat Completions",
          apiProtocol: "rest",
          abstract: true,
        },
      ],
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
assert.deepEqual(matches[0].requiredApi, {
  ordId: "example:apiResource:chat:v1",
  title: "OpenAI-compatible Chat Completions",
  apiProtocol: "rest",
  abstract: true,
});
assert.deepEqual(
  matches[0].candidates.map(({ provider, api }) => [provider.name, api.ordId]),
  [["private-llm", "example:apiResource:implementation:v1"]],
);

const manyProviders = Array.from({ length: 12 }, (_, index) => ({
  provider: {
    name: `provider-${index}`,
    providerMetadata: { spec: { displayName: `Provider ${index}` } },
  },
  documents: [
    {
      apiResources: [
        {
          ordId: `example:apiResource:implementation-${index}:v1`,
          visibility: "public",
          compatibleWith: [
            { ordId: "example:apiResource:chat:v1", maxVersion: "1.0" },
          ],
        },
      ],
    },
  ],
}));

const orderedMatches = matchProviders(
  current,
  currentDocuments(),
  manyProviders,
);
assert.deepEqual(
  orderedMatches[0].candidates.map(({ provider }) => provider.name),
  manyProviders.map(({ provider }) => provider.name),
);

const groupedCandidates = groupCandidatesByProvider([
  orderedMatches[0].candidates[0],
  orderedMatches[0].candidates[0],
  {
    provider: orderedMatches[0].candidates[0].provider,
    api: { ordId: "example:apiResource:second-implementation:v1" },
  },
  orderedMatches[0].candidates[1],
]);
assert.deepEqual(
  groupedCandidates.map(({ provider, apis }) => [
    provider.name,
    apis.map(({ ordId }) => ordId),
  ]),
  [
    [
      "provider-0",
      [
        "example:apiResource:implementation-0:v1",
        "example:apiResource:second-implementation:v1",
      ],
    ],
    ["provider-1", ["example:apiResource:implementation-1:v1"]],
  ],
);

function currentDocuments() {
  return [
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
  ];
}
