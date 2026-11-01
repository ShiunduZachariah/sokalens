/**
 * Which of the match's three readings a surface is showing.
 *
 * In its own module, rather than exported from the in-play board that first needed
 * it, for two reasons. It is a domain concept — the match is before, during or
 * after — and several surfaces now key off it. And a `.ts` module can be imported
 * directly by the Node test runner, which a `.tsx` one cannot, so anything the
 * scoreboard's pure functions depend on has to live outside a component file.
 */
export type MatchPhase = "prematch" | "live" | "finished";
