// Match the account API's JSON budget before allocating an upstream body.
export const accountBodyLimit = 64 * 1024;

export class AccountBodyError extends Error {
  status: 400 | 408 | 413;

  constructor(status: 400 | 408 | 413, message: string) {
    super(message);
    this.status = status;
  }
}

export async function readAccountBody(request: Request, timeoutMs = 2_800): Promise<ArrayBuffer> {
  const declared = request.headers.get("content-length");
  if (declared !== null && (!/^\d+$/.test(declared) || Number(declared) > accountBodyLimit)) {
    // Cancellation must not hold the response open if the sender stalls.
    void request.body?.cancel().catch(() => {});
    throw new AccountBodyError(/^\d+$/.test(declared) ? 413 : 400, "Invalid account request size.");
  }
  if (!request.body) return new ArrayBuffer(0);

  const reader = request.body.getReader();
  const bytes = new Uint8Array(accountBodyLimit);
  let size = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let onAbort: () => void = () => {};
  const interrupted = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new AccountBodyError(408, "Account request body timed out.")), timeoutMs);
    onAbort = () => reject(new AccountBodyError(400, "Account request was interrupted."));
    request.signal.addEventListener("abort", onAbort, { once: true });
    if (request.signal.aborted) onAbort();
  });

  try {
    while (true) {
      const chunk = await Promise.race([interrupted, reader.read()]);
      if (chunk.done) return bytes.buffer.slice(0, size);
      if (chunk.value.byteLength > accountBodyLimit - size) {
        throw new AccountBodyError(413, "Account request body is too large.");
      }
      bytes.set(chunk.value, size);
      size += chunk.value.byteLength;
    }
  } catch (error) {
    void reader.cancel().catch(() => {});
    throw error;
  } finally {
    clearTimeout(timer);
    request.signal.removeEventListener("abort", onAbort);
    reader.releaseLock();
  }
}
