// Login Secrets page of the Chat UI provider. The portal's generic list cannot
// filter Secrets by label and would list every Secret of the account, so this
// page asks the portal's GraphQL gateway for the operator-labelled login
// Secrets only and shows each one next to the instance it belongs to.

const LOGIN_SECRET_SELECTOR = "ui.privatellms.msp/login-secret=true";
const MASK = "••••••••••••";

export const QUERY = `query {
  ui_privatellms_msp { v1alpha1 { ChatUIInstances { items {
    metadata { name namespace }
    status { url loginSecretName }
  } } } }
  v1 { Secrets(labelselector: "${LOGIN_SECRET_SELECTOR}") { items {
    metadata { name namespace }
    data
  } } }
}`;

// gatewayFor returns the account's GraphQL endpoint, or undefined when it is
// not on the origin that sent the context: the portal's token is only ever
// sent back to the portal that supplied it.
export function gatewayFor(context, parentOrigin) {
  try {
    const url = new URL(context?.portalContext?.crdGatewayApiUrl);
    if (url.origin !== parentOrigin) return undefined;
    // Like the portal's own views, address the workspace of the current entity.
    const path = context.kcpPath;
    if (typeof path === "string" && /^[A-Za-z0-9:-]+$/.test(path)) {
      url.pathname = url.pathname.replace(
        /\/clusters\/[^/]+\//,
        `/clusters/${path}/`,
      );
    }
    return url.href;
  } catch {
    return undefined;
  }
}

function httpsUrl(value) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" ? url.href : undefined;
  } catch {
    return undefined;
  }
}

function decode(value) {
  try {
    const bytes = Uint8Array.from(atob(value), (char) => char.charCodeAt(0));
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return undefined;
  }
}

// rowsFrom joins each login Secret with the instance whose status names it.
export function rowsFrom(data) {
  const instances =
    data?.ui_privatellms_msp?.v1alpha1?.ChatUIInstances?.items ?? [];
  const owners = new Map();
  for (const instance of instances) {
    const login = instance?.status?.loginSecretName;
    if (login) owners.set(`${instance.metadata.namespace}/${login}`, instance);
  }
  return (data?.v1?.Secrets?.items ?? [])
    .map((secret) => {
      const { name, namespace } = secret.metadata;
      const owner = owners.get(`${namespace}/${name}`);
      return {
        instance: owner?.metadata?.name,
        url: httpsUrl(owner?.status?.url),
        namespace,
        secret: name,
        password: decode(secret.data?.password),
      };
    })
    .sort(
      (a, b) =>
        // Secrets without an instance go last.
        Number(!a.instance) - Number(!b.instance) ||
        (a.instance ?? "").localeCompare(b.instance ?? "") ||
        a.namespace.localeCompare(b.namespace) ||
        a.secret.localeCompare(b.secret),
    );
}

export async function load(context, parentOrigin, fetchImpl = fetch) {
  const gateway = gatewayFor(context, parentOrigin);
  if (!gateway || typeof context?.token !== "string" || !context.token) {
    throw new Error("The portal did not provide access to this account.");
  }
  const response = await fetchImpl(gateway, {
    method: "POST",
    credentials: "omit",
    headers: {
      Authorization: `Bearer ${context.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ query: QUERY }),
    signal: AbortSignal.timeout(15000),
  });
  if (!response.ok) {
    throw new Error(`The portal answered with status ${response.status}.`);
  }
  const result = await response.json();
  // Instances may be unreadable for this user; the Secrets are required.
  if (!result?.data?.v1?.Secrets) {
    throw new Error("The portal returned no Secrets for this account.");
  }
  return rowsFrom(result.data);
}

function element(tag, className, content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
}

async function copyText(value) {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    // The portal's frame may not delegate clipboard access.
  }
  const area = element("textarea", "clipboard");
  area.value = value;
  document.body.append(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
  }
}

function passwordCell(row) {
  const cell = element("td", "password");
  if (row.password === undefined) {
    cell.append(element("span", "muted", "Not readable"));
    return cell;
  }
  const value = element("span", "password__value", MASK);
  const reveal = element("button", "action");
  reveal.type = "button";
  const show = (shown) => {
    value.textContent = shown ? row.password : MASK;
    reveal.textContent = shown ? "Hide" : "Show";
    reveal.setAttribute(
      "aria-label",
      `${reveal.textContent} the password of ${row.secret}`,
    );
  };
  show(false);
  reveal.addEventListener("click", () => show(value.textContent === MASK));
  const copy = element("button", "action", "Copy");
  copy.type = "button";
  copy.setAttribute("aria-label", `Copy the password of ${row.secret}`);
  copy.addEventListener("click", async () => {
    copy.textContent = (await copyText(row.password)) ? "Copied" : "Failed";
    window.setTimeout(() => (copy.textContent = "Copy"), 2000);
  });
  cell.append(value, reveal, copy);
  return cell;
}

function instanceCell(row) {
  const cell = element("td");
  if (!row.instance) {
    cell.append(element("span", "muted", "—"));
  } else if (row.url) {
    const link = element("a", "link", row.instance);
    link.href = row.url;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    cell.append(link);
  } else {
    cell.textContent = row.instance;
  }
  return cell;
}

function renderRows(app, rows) {
  const card = element("section", "card");
  card.append(element("h1", "card__title", "Login Secrets"));
  if (rows.length === 0) {
    card.append(
      element(
        "p",
        "empty",
        "No Chat UI instance in this account signs in with a generated password.",
      ),
    );
    app.replaceChildren(card);
    return;
  }
  const table = element("table", "table");
  const head = element("tr");
  for (const title of ["Chat UI", "Namespace", "Login Secret", "Password"]) {
    const cell = element("th", undefined, title);
    cell.scope = "col";
    head.append(cell);
  }
  const thead = element("thead");
  thead.append(head);
  const body = element("tbody");
  for (const row of rows) {
    const line = element("tr");
    line.append(
      instanceCell(row),
      element("td", undefined, row.namespace),
      element("td", "name", row.secret),
      passwordCell(row),
    );
    body.append(line);
  }
  table.append(thead, body);
  card.append(table);
  app.replaceChildren(card);
}

function renderMessage(app, className, message) {
  app.replaceChildren(element("p", className, message));
}

function start() {
  const app = document.getElementById("app");
  let latest = 0;
  window.addEventListener("message", async (event) => {
    if (event.source !== window.parent || event.data?.msg !== "luigi.init") {
      return;
    }
    window.parent.postMessage({ msg: "luigi.init.ok" }, event.origin);
    const request = ++latest;
    try {
      const raw = event.data.context;
      const context = typeof raw === "string" ? JSON.parse(raw) : raw;
      const rows = await load(
        { token: event.data.authData?.accessToken, ...context },
        event.origin,
      );
      if (request === latest) renderRows(app, rows);
    } catch (error) {
      if (request === latest) {
        renderMessage(
          app,
          "error",
          `Could not load the login Secrets. ${error?.message ?? ""}`.trim(),
        );
      }
    }
  });
  window.parent.postMessage(
    { msg: "luigi.get-context", clientVersion: "2.22.1" },
    "*",
  );
}

if (typeof window !== "undefined") {
  if (window.parent !== window) {
    start();
  } else {
    renderMessage(
      document.getElementById("app"),
      "empty",
      "Open this page from the Chat UI group in the portal.",
    );
  }
}
