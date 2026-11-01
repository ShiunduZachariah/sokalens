type TimerAPI = {
  set: (callback: () => void, delayMs: number) => number;
  clear: (id: number) => void;
};

/** A slot's fill budget starts only when its adapter actually starts loading. */
export function createAdAttemptTimer(
  timeoutMs: number,
  onTimeout: () => void,
  timer: TimerAPI = {
    set: (callback, delayMs) => window.setTimeout(callback, delayMs),
    clear: (id) => window.clearTimeout(id),
  },
) {
  let started = false;
  let stopped = false;
  let id: number | null = null;
  return {
    start() {
      if (started || stopped) return false;
      started = true;
      id = timer.set(() => {
        id = null;
        if (!stopped) onTimeout();
      }, timeoutMs);
      return true;
    },
    stop() {
      stopped = true;
      if (id !== null) timer.clear(id);
      id = null;
    },
  };
}
