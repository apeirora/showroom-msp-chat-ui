const PROTOCOL = "platform-mesh.provider-details.v1";
const NAVIGATE = "platform-mesh.provider-details.navigate.v1";
const RESIZE = "platform-mesh.provider-details.resize.v1";
const MAX_DOCUMENTS = 10;
const MAX_BYTES = 2 * 1024 * 1024;

export function parseProviderData(data) {
  if (!data) return {};
  if (typeof data === "object") return data;
  if (typeof data !== "string") return {};
  try {
    const parsed = JSON.parse(data);
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

export function configUrlFor(provider) {
  const data = parseProviderData(provider?.providerMetadata?.spec?.data);
  const url = data?.ord?.configUrl;
  if (typeof url !== "string") return undefined;
  try {
    const parsed = new URL(url);
    return ["http:", "https:"].includes(parsed.protocol)
      ? parsed.href
      : undefined;
  } catch {
    return undefined;
  }
}

export function matchesVersion(minVersion, maxVersion) {
  if (!minVersion) return true;
  if (!maxVersion) return false;
  const required = versionPair(minVersion);
  const offered = versionPair(maxVersion);
  if (!required || !offered || required[0] !== offered[0]) return false;
  return offered[1] >= required[1];
}

export function matchProviders(
  currentProvider,
  currentDocuments,
  providerDocuments,
) {
  const dependencies = currentDocuments.flatMap(
    (document) => document.integrationDependencies ?? [],
  );
  const requirements = dependencies.flatMap((dependency) =>
    (dependency.aspects ?? []).flatMap((aspect) =>
      (aspect.apiResources ?? []).map((resource) => ({
        dependency,
        aspect,
        resource,
      })),
    ),
  );

  return requirements.map((requirement) => ({
    ...requirement,
    candidates: providerDocuments.flatMap(({ provider, documents }) => {
      if (provider.name === currentProvider.name) return [];
      return documents.flatMap((document) =>
        (document.apiResources ?? [])
          .filter(
            (api) =>
              api.visibility === "public" &&
              !api.abstract &&
              (api.compatibleWith ?? []).some(
                (compatibility) =>
                  compatibility.ordId === requirement.resource.ordId &&
                  matchesVersion(
                    requirement.resource.minVersion,
                    compatibility.maxVersion,
                  ),
              ),
          )
          .map((api) => ({ provider, api })),
      );
    }),
  }));
}

export async function loadProviderDocuments(provider, fetchJSON = fetchJson) {
  const configUrl = configUrlFor(provider);
  if (!configUrl) return [];
  const configuration = await fetchJSON(configUrl, 256 * 1024);
  const declarations = configuration?.openResourceDiscoveryV1?.documents;
  if (!Array.isArray(declarations))
    throw new Error("Invalid ORD configuration");
  const openDocuments = declarations
    .filter((document) =>
      (document.accessStrategies ?? []).some(
        (strategy) => strategy.type === "open",
      ),
    )
    .slice(0, MAX_DOCUMENTS);

  return Promise.all(
    openDocuments.map(async (document) => {
      const result = await fetchJSON(
        new URL(document.url, configUrl).href,
        MAX_BYTES,
      );
      if (result?.openResourceDiscovery !== "1.16") {
        throw new Error("Unsupported ORD version");
      }
      return result;
    }),
  );
}

async function fetchJson(url, maxBytes) {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch(url, {
      credentials: "omit",
      redirect: "error",
      referrerPolicy: "no-referrer",
      signal: controller.signal,
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const contentType = response.headers.get("content-type") ?? "";
    if (
      !contentType.includes("application/json") &&
      !contentType.includes("+json")
    ) {
      throw new Error("Response is not JSON");
    }
    const text = await response.text();
    if (new TextEncoder().encode(text).byteLength > maxBytes) {
      throw new Error("Response is too large");
    }
    return JSON.parse(text);
  } finally {
    window.clearTimeout(timeout);
  }
}

function versionPair(version) {
  const match = /^(\d+)\.(\d+)(?:\.\d+)?$/.exec(version);
  return match ? [Number(match[1]), Number(match[2])] : undefined;
}

function text(element, value) {
  element.textContent = value ?? "";
  return element;
}

function element(tag, className, value) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (value !== undefined) text(node, value);
  return node;
}

function render(context) {
  const app = document.querySelector("#app");
  if (!app) return;
  app.replaceChildren();

  if (context?.protocolVersion !== PROTOCOL) {
    app.append(element("p", "error", "Provider details are unavailable."));
    return;
  }

  Promise.allSettled(
    context.providers.map(async (provider) => ({
      provider,
      documents: await loadProviderDocuments(provider),
    })),
  ).then((results) => {
    const loaded = results
      .filter((result) => result.status === "fulfilled")
      .map((result) => result.value);
    const current = loaded.find(
      ({ provider }) => provider.name === context.currentProvider.name,
    );
    app.replaceChildren();
    if (!current) {
      app.append(
        element("p", "error", "Additional service information is unavailable."),
      );
      return;
    }

    const matches = matchProviders(
      context.currentProvider,
      current.documents,
      loaded,
    );
    if (!matches.length) return;

    app.append(element("h2", "", "Required implementations"));
    for (const match of matches) {
      const requirement = element("section", "requirement");
      requirement.append(
        element(
          "p",
          "eyebrow",
          match.dependency.mandatory ? "Mandatory" : "Optional",
        ),
        element(
          "h3",
          "",
          match.dependency.title || match.aspect.title || "Required API",
        ),
        element("p", "contract", match.resource.ordId),
      );
      if (match.resource.minVersion) {
        requirement.append(
          element("p", "meta", `Minimum version ${match.resource.minVersion}`),
        );
      }
      app.append(requirement);

      const resultsSection = element("section", "results");
      if (!match.candidates.length) {
        resultsSection.append(
          element(
            "p",
            "empty",
            "No compatible offering was found in this marketplace.",
          ),
        );
      }
      for (const candidate of match.candidates) {
        resultsSection.append(renderCandidate(candidate));
      }
      app.append(resultsSection);
    }
    app.append(
      element(
        "p",
        "notice",
        "Compatibility is declared by provider ORD metadata; it is not a runtime conformance check.",
      ),
    );
  });
}

function renderCandidate({ provider, api }) {
  const card = element("article", "result");
  const head = element("div", "result__head");
  const title = element("div");
  title.append(
    element("h3", "", provider.providerMetadata.spec.displayName),
    element("p", "meta", api.title || api.ordId),
  );
  const button = element("button", "", "View details");
  button.type = "button";
  button.addEventListener("click", () =>
    sendMessage(NAVIGATE, { providerName: provider.name }),
  );
  head.append(title, button);
  card.append(head);
  const badges = element("div", "badges");
  for (const value of [
    "Contract match",
    api.apiProtocol?.toUpperCase(),
    api.version,
  ].filter(Boolean)) {
    badges.append(element("span", "badge", value));
  }
  card.append(badges);
  return card;
}

let targetOrigin = "*";

function sendMessage(id, data) {
  window.parent.postMessage(
    { msg: "custom", data: { id, ...data } },
    targetOrigin,
  );
}

function reportHeight() {
  sendMessage(RESIZE, { height: document.documentElement.scrollHeight });
}

function initLuigiClient() {
  window.addEventListener("message", (event) => {
    if (event.source !== window.parent || event.data?.msg !== "luigi.init")
      return;
    targetOrigin = event.origin;
    const rawContext = event.data.context;
    window.parent.postMessage({ msg: "luigi.init.ok" }, targetOrigin);
    try {
      const context =
        typeof rawContext === "string" ? JSON.parse(rawContext) : rawContext;
      render(context);
    } catch {
      render(undefined);
    }
  });
  window.parent.postMessage(
    { msg: "luigi.get-context", clientVersion: "2.22.1" },
    "*",
  );
  new ResizeObserver(reportHeight).observe(document.documentElement);
}

if (typeof window !== "undefined" && window.parent !== window) {
  initLuigiClient();
}
