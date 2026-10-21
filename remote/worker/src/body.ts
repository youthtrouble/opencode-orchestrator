import { HttpError } from "./http.ts";

export const MAX_BODY_BYTES = 256 * 1024;

export function payloadTooLarge(): HttpError {
  return new HttpError(413, "payload_too_large", `request body exceeds ${MAX_BODY_BYTES} bytes`);
}

/**
 * Reads and parses a JSON body, stopping as soon as it passes MAX_BODY_BYTES. The gate already
 * rejects a declared Content-Length over the cap; this catches a body without one, or one that lies.
 * Every handler (WT8 machine, WT9 browser) must read its body through this, never `request.json()`
 * or `request.text()`, or the actual-bytes cap does not apply.
 */
export async function readJsonBody(request: Request): Promise<unknown> {
  const text = await readCappedText(request);
  try {
    return JSON.parse(text);
  } catch {
    throw new HttpError(400, "invalid_json", "request body is not valid JSON");
  }
}

async function readCappedText(request: Request): Promise<string> {
  if (!request.body) return "";
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let received = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    received += value.byteLength;
    if (received > MAX_BODY_BYTES) {
      await reader.cancel();
      throw payloadTooLarge();
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(received);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(bytes);
}
