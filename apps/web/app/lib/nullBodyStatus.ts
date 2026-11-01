/**
 * Statuses the Fetch spec forbids a body on.
 *
 * `new Response(body, { status })` throws a TypeError for these, so the
 * account proxy has to pass `null` rather than an empty buffer. Otherwise a
 * perfectly good upstream 204 is caught as a transport failure and reported
 * to the browser as a 503 -- which is exactly what happened to every
 * favorite removal, the only DELETE the web app issues.
 */
export const nullBodyStatuses = new Set([101, 103, 204, 205, 304]);

export function nullBodyStatus(status: number) {
  return nullBodyStatuses.has(status);
}
