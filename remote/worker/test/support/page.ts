// The Work Trains page (public/app.js) run once against a fake DOM, a fake fetch that serves one summary
// and records every request, and a fake clipboard that records every write. The fake follows the DOM
// where the page relies on it: append and replaceChildren turn any argument that is not a node into text
// with String(), so a stray null shows as "null", as in a browser.

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
  private readonly listeners = new Map<string, ((event: { target: FakeNode }) => void)[]>();
  hidden = false;
  /** A `details` element's open state, which the browser flips before it fires `toggle`. */
  open = false;

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

  addEventListener(type: string, listener: (event: { target: FakeNode }) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  dispatch(type: string) {
    for (const listener of this.listeners.get(type) ?? []) listener({ target: this });
  }

  /** This node and every element under it that `matches`, in document order. */
  findAll(matches: (node: FakeNode) => boolean): FakeNode[] {
    const below = this.children.flatMap((child) => (child instanceof FakeNode ? child.findAll(matches) : []));
    return matches(this) ? [this, ...below] : below;
  }
}

const asChild = (node: unknown): Child =>
  node instanceof FakeNode || node instanceof FakeText ? node : new FakeText(String(node));

/** A request the page sent: its method, path and parsed JSON body. */
export interface SentRequest {
  method: string;
  path: string;
  body: unknown;
}

/** navigator.clipboard: keeps what the page wrote, or refuses as a browser does without permission. */
export class FakeClipboard {
  readonly written: string[] = [];
  refuses = false;

  async writeText(text: string) {
    if (this.refuses) throw new DOMException("Write permission denied.", "NotAllowedError");
    this.written.push(text);
  }
}

export class FakeDocument {
  readonly activeElement = null;
  readonly requests: SentRequest[] = [];
  readonly clipboard = new FakeClipboard();
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

/** Where the page is open unless a test says otherwise: the summaries' PUBLIC_ORIGIN, when they name one. */
export const PAGE_ORIGIN = "https://trains.example.com";

/**
 * Loads a fresh copy of the page from `origin`, lets it render `summary`, runs `interact` (clicks and
 * the like, while the page's globals are still in place), waits for what that started, and returns
 * its document.
 */
export async function pageShowing(
  summary: object,
  origin = PAGE_ORIGIN,
  interact: (document: FakeDocument) => void | Promise<void> = () => {},
) {
  const document = new FakeDocument();
  const globals = globalThis as Record<string, unknown>;
  const saved = {
    document: globals.document,
    fetch: globals.fetch,
    setInterval: globals.setInterval,
    location: globals.location,
  };
  Object.assign(globals, {
    document,
    location: { origin },
    fetch: async (path: string, init: { method: string; body?: string }) => {
      document.requests.push({ method: init.method, path, body: init.body === undefined ? undefined : JSON.parse(init.body) });
      return { ok: true, status: 200, type: "basic", json: async () => summary };
    },
    setInterval: () => 0,
  });
  // Node defines navigator as a getter without a setter, so it is redefined rather than assigned.
  const savedNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  Object.defineProperty(globalThis, "navigator", { value: { clipboard: document.clipboard }, configurable: true });
  try {
    // A distinct URL is a distinct module instance, so each load starts from an empty page.
    await import(`../../public/app.js?load=${++loads}`);
    await settle();
    await interact(document);
    await settle();
  } finally {
    Object.assign(globals, saved);
    if (savedNavigator) Object.defineProperty(globalThis, "navigator", savedNavigator);
    else delete globals.navigator;
  }
  return document;
}

/** Lets the page's pending promise chains run out. */
async function settle() {
  for (let i = 0; i < 10; i++) await new Promise((resolve) => setImmediate(resolve));
}
