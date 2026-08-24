const PROTOCOL = "platform-mesh.provider-details.v1";
const NAVIGATE = "platform-mesh.provider-details.navigate.v1";
const RESIZE = "platform-mesh.provider-details.resize.v1";
const MAX_DOCUMENTS = 10;
const MAX_BYTES = 2 * 1024 * 1024;
const DOCUMENT_URL = Symbol("documentUrl");

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

export function providerIconUrl(provider, prefersDark = false) {
  const icon = provider?.providerMetadata?.spec?.icon;
  const preferred = prefersDark ? icon?.dark : icon?.light;
  const fallback = prefersDark ? icon?.light : icon?.dark;
  return [
    preferred?.url,
    preferred?.data,
    fallback?.url,
    fallback?.data,
  ]
    .map(safeImageUrl)
    .find(Boolean);
}

export function resourceDefinitionUrlFor(api, baseUrl) {
  const definition = (api?.resourceDefinitions ?? []).find(
    (candidate) =>
      typeof candidate.url === "string" &&
      (candidate.accessStrategies ?? []).some(
        (strategy) => strategy.type === "open",
      ),
  );
  if (!definition || !baseUrl) return undefined;
  try {
    const url = new URL(definition.url, baseUrl);
    return ["http:", "https:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}

function safeImageUrl(value) {
  if (typeof value !== "string") return undefined;
  if (
    /^data:image\/(?:gif|jpeg|png|svg\+xml|webp);base64,[a-z0-9+/=\s]+$/i.test(
      value,
    )
  ) {
    return value;
  }
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" ? parsed.href : undefined;
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
      (document.apiResources ?? []).map((api) => [
        api.ordId,
        { api, documentUrl: document[DOCUMENT_URL] },
      ]),
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
        requiredApi: apiResources.get(resource.ordId)?.api,
        requiredApiDocumentUrl: apiResources.get(resource.ordId)?.documentUrl,
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
    if (!group.apis.some((api) => api.ordId === candidate.api.ordId)) {
      group.apis.push(candidate.api);
    }
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
      const documentUrl = new URL(document.url, configUrl).href;
      const result = await fetchJSON(
        documentUrl,
        MAX_BYTES,
      );
      if (result?.openResourceDiscovery !== "1.16") {
        throw new Error("Unsupported ORD version");
      }
      Object.defineProperty(result, DOCUMENT_URL, { value: documentUrl });
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

    for (const match of matches) {
      const candidateGroups = groupCandidatesByProvider(match.candidates);
      const matchSection = element("div", "match");

      const apiSection = element("section", "panel api-panel");
      const apiHeader = element("header", "panel__header");
      const apiTitle = element("div", "panel__title");
      apiTitle.append(
        element(
          "h2",
          "eyebrow",
          match.dependency.mandatory === false ? "Optional API" : "Required API",
        ),
        element(
          "p",
          "panel__name",
          match.requiredApi?.title || match.aspect.title || "API",
        ),
      );
      const apiBadges = element("div", "badges");
      if (match.requiredApi?.apiProtocol) {
        apiBadges.append(
          element("span", "badge", match.requiredApi.apiProtocol.toUpperCase()),
        );
      }
      if (match.resource.minVersion) {
        apiBadges.append(
          element(
            "span",
            "badge badge--neutral",
            `v${match.resource.minVersion}+`,
          ),
        );
      }
      apiHeader.append(apiTitle, apiBadges);
      const definitionUrl = resourceDefinitionUrlFor(
        match.requiredApi,
        match.requiredApiDocumentUrl || configUrlFor(context.currentProvider),
      );
      const apiId = definitionUrl
        ? element("a", "api-panel__id api-panel__link", match.resource.ordId)
        : element("code", "api-panel__id", match.resource.ordId);
      if (definitionUrl) {
        apiId.href = definitionUrl;
        apiId.target = "_blank";
        apiId.rel = "noopener noreferrer";
        apiId.title = "Open API definition in a new tab";
        apiId.setAttribute(
          "aria-label",
          `${match.resource.ordId} — open API definition in a new tab`,
        );
      }
      apiSection.append(
        apiHeader,
        apiId,
      );

      const servicesSection = element("section", "panel services-panel");
      const servicesHeader = element("header", "panel__header");
      const servicesTitle = element("div", "panel__title");
      servicesTitle.append(
        element("h2", "eyebrow", "Supported services"),
        element(
          "p",
          "panel__name",
          match.dependency.title || match.aspect.title || "Services",
        ),
      );
      const providerLabel =
        candidateGroups.length === 1 ? "provider" : "providers";
      servicesHeader.append(
        servicesTitle,
        element(
          "span",
          "count",
          `${candidateGroups.length} ${providerLabel}`,
        ),
      );
      servicesSection.append(servicesHeader);

      const resultsSection = element("div", "results");
      if (!candidateGroups.length) {
        resultsSection.append(element("p", "empty", "No compatible providers"));
      }
      for (const candidateGroup of candidateGroups) {
        resultsSection.append(renderCandidate(candidateGroup));
      }
      servicesSection.append(resultsSection);
      matchSection.append(apiSection, servicesSection);
      app.append(matchSection);
    }
  });
}

function renderCandidate({ provider, apis }) {
  const card = element("button", "result");
  card.type = "button";
  card.addEventListener("click", () =>
    sendMessage(NAVIGATE, { providerName: provider.name }),
  );
  const displayName = provider.providerMetadata.spec.displayName;
  card.append(renderProviderIcon(provider, displayName));
  const title = element("span", "result__title");
  title.append(
    element("strong", "", displayName),
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

function renderProviderIcon(provider, displayName) {
  const visual = element("span", "provider-visual");
  visual.setAttribute("aria-hidden", "true");
  const initials = (displayName || provider.name)
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0])
    .join("")
    .toUpperCase();
  const showFallback = () => {
    visual.replaceChildren();
    visual.classList.add("provider-visual--fallback");
    text(visual, initials || "MSP");
  };
  const iconUrl = providerIconUrl(provider);
  if (iconUrl) {
    const icon = element("img", "provider-icon");
    icon.src = iconUrl;
    icon.alt = "";
    icon.loading = "lazy";
    icon.decoding = "async";
    icon.referrerPolicy = "no-referrer";
    icon.addEventListener("error", showFallback, { once: true });
    visual.append(icon);
    return visual;
  }
  showFallback();
  return visual;
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
