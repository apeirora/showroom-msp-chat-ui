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
  const apiResources = new Map(
    currentDocuments.flatMap((document) =>
      (document.apiResources ?? []).map((api) => [api.ordId, api]),
    ),
  );
  const dependencies = currentDocuments.flatMap(
    (document) => document.integrationDependencies ?? [],
  );
  const requirements = dependencies.flatMap((dependency) =>
    (dependency.aspects ?? []).flatMap((aspect) =>
      (aspect.apiResources ?? []).map((resource) => ({
        dependency,
        aspect,
        resource,
        requiredApi: apiResources.get(resource.ordId),
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

export function groupCandidatesByProvider(candidates) {
  const groups = [];
  const byName = new Map();
  for (const candidate of candidates) {
    let group = byName.get(candidate.provider.name);
    if (!group) {
      group = { provider: candidate.provider, apis: [] };
      byName.set(candidate.provider.name, group);
      groups.push(group);
    }
    group.apis.push(candidate.api);
  }
  return groups;
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

export function render(context) {
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

    app.append(element("h2", "", "ORD matches"));
    for (const match of matches) {
      const candidateGroups = groupCandidatesByProvider(match.candidates);
      const compatibility = element("section", "compatibility");
      const requirement = element("header", "requirement");
      const requirementTitle = element("div", "requirement__title");
      requirementTitle.append(
        element(
          "p",
          "eyebrow",
          match.dependency.mandatory ? "Required" : "Optional",
        ),
        element(
          "h3",
          "",
          match.dependency.title || match.aspect.title || "Required API",
        ),
      );
      const providerLabel =
        candidateGroups.length === 1 ? "provider" : "providers";
      requirement.append(
        requirementTitle,
        element(
          "span",
          "count",
          `${candidateGroups.length} ${providerLabel}`,
        ),
      );
      const contract = element("div", "contract");
      const contractDetails = element("div", "contract__details");
      contractDetails.append(
        element("span", "eyebrow", "Required API"),
        element(
          "strong",
          "contract__title",
          match.requiredApi?.title || match.aspect.title || "API",
        ),
        element("code", "", match.resource.ordId),
      );
      const contractBadges = element("div", "badges");
      if (match.requiredApi?.apiProtocol) {
        contractBadges.append(
          element("span", "badge", match.requiredApi.apiProtocol.toUpperCase()),
        );
      }
      if (match.resource.minVersion) {
        contractBadges.append(
          element(
            "span",
            "badge badge--neutral",
            `v${match.resource.minVersion}+`,
          ),
        );
      }
      contract.append(contractDetails, contractBadges);
      compatibility.append(requirement, contract);

      const resultsSection = element("div", "results");
      if (!candidateGroups.length) {
        resultsSection.append(element("p", "empty", "No compatible providers"));
      }
      for (const candidateGroup of candidateGroups) {
        resultsSection.append(renderCandidate(candidateGroup));
      }
      compatibility.append(resultsSection);
      app.append(compatibility);
    }
  });
}

function renderCandidate({ provider, apis }) {
  const card = element("button", "result");
  card.type = "button";
  card.addEventListener("click", () =>
    sendMessage(NAVIGATE, { providerName: provider.name }),
  );
  const title = element("span", "result__title");
  title.append(
    element("strong", "", provider.providerMetadata.spec.displayName),
    element(
      "span",
      "meta",
      apis.map((api) => api.title || api.ordId).join(" · "),
    ),
  );
  const badges = element("div", "badges");
  const badgeValues = new Set(
    apis.flatMap((api) => [api.apiProtocol?.toUpperCase(), api.version]),
  );
  for (const value of badgeValues) {
    if (!value) continue;
    badges.append(element("span", "badge", value));
  }
  const arrow = element("span", "arrow", "›");
  arrow.setAttribute("aria-hidden", "true");
  card.append(title, badges, arrow);
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
