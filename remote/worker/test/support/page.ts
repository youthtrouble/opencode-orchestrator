// The Work Trains page (public/app.js) run once against a fake DOM and a fake fetch that serves one
// summary. The fake follows the DOM where the page relies on it: append and replaceChildren turn any
// argument that is not a node into text with String(), so a stray null shows as "null", as in a browser.

type Child = FakeNode | FakeText;

export class FakeText {
  readonly data: string;
  constructor(data: string) {
    this.data = data;
  }
  get textContent() {
    return this.data;
  }
}

export class FakeNode {
  readonly children: Child[] = [];
  readonly attributes = new Map<string, string>();
  readonly dataset: Record<string, string> = {};
  readonly tagName: string;
  hidden = false;

  constructor(tagName: string) {
    this.tagName = tagName;
  }

  get textContent(): string {
    return this.children.map((child) => child.textContent).join("");
  }

  set textContent(text: string) {
    this.replaceChildren(text);
  }

  append(...nodes: unknown[]) {
    this.children.push(...nodes.map(asChild));
  }

  replaceChildren(...nodes: unknown[]) {
    this.children.splice(0, this.children.length, ...nodes.map(asChild));
  }

  setAttribute(name: string, value: unknown) {
    this.attributes.set(name, String(value));
  }

  addEventListener() {}
}

const asChild = (node: unknown): Child =>
  node instanceof FakeNode || node instanceof FakeText ? node : new FakeText(String(node));

class FakeDocument {
  readonly activeElement = null;
  // Hidden, so the page schedules no next poll and the test process can exit.
  readonly visibilityState = "hidden";
  private readonly byId = new Map<string, FakeNode>();

  getElementById(id: string) {
    if (!this.byId.has(id)) this.byId.set(id, new FakeNode("div"));
    return this.byId.get(id)!;
  }

  createElement(tag: string) {
    return new FakeNode(tag);
  }

  querySelectorAll() {
    return [];
  }

  addEventListener() {}
}

let loads = 0;

/** Loads a fresh copy of the page, lets it render `summary`, and returns its document. */
export async function pageShowing(summary: object) {
  const document = new FakeDocument();
  const globals = globalThis as Record<string, unknown>;
  const saved = { document: globals.document, fetch: globals.fetch, setInterval: globals.setInterval };
  Object.assign(globals, {
    document,
    fetch: async () => ({ ok: true, status: 200, type: "basic", json: async () => summary }),
    setInterval: () => 0,
  });
  try {
    // A distinct URL is a distinct module instance, so each load starts from an empty page.
    await import(`../../public/app.js?load=${++loads}`);
    await new Promise((resolve) => setImmediate(resolve));
  } finally {
    Object.assign(globals, saved);
  }
  return document;
}
