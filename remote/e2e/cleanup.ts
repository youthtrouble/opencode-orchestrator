// One teardown for an e2e run, shared by the test's `after` hook and by SIGINT/SIGTERM. node:test runs `after`
// only when the file finishes, so an interrupted run would otherwise leave the detached `wrangler dev` listening
// and the run's temp directory on disk. Importing this module changes nothing; a run opts in with
// releaseOnSignals().
const SIGNALS = ["SIGINT", "SIGTERM"] as const;

type Step = () => void | Promise<void>;

export class RunCleanup {
  readonly #steps: Step[] = [];
  #run: Promise<void> | undefined;
  readonly #onSignal = (signal: NodeJS.Signals) => this.#releaseThenDie(signal);

  /** Adds a step. Steps run last-added first, so a resource goes before the directory it lives in. */
  defer(step: Step): void {
    if (this.#run) throw new Error("cleanup already ran; release this resource directly");
    this.#steps.push(step);
  }

  /** Runs every step once, however often it is called; a failed step does not stop the rest. */
  run(): Promise<void> {
    this.#run ??= this.#runSteps();
    return this.#run;
  }

  /**
   * Until the cleanup has run, SIGINT and SIGTERM run it and then end the process by the same signal, so the
   * caller still sees an interrupted run.
   */
  releaseOnSignals(): void {
    for (const signal of SIGNALS) process.on(signal, this.#onSignal);
  }

  async #runSteps(): Promise<void> {
    const errors: unknown[] = [];
    for (const step of this.#steps.toReversed()) {
      try {
        await step();
      } catch (error) {
        errors.push(error);
      }
    }
    for (const signal of SIGNALS) process.off(signal, this.#onSignal);
    if (errors.length === 1) throw errors[0];
    if (errors.length > 1) throw new AggregateError(errors, "e2e cleanup failed");
  }

  #releaseThenDie(signal: NodeJS.Signals): void {
    this.run()
      .catch((error: unknown) => console.error("e2e cleanup failed:", error))
      .finally(() => {
        // The handlers are gone once the cleanup ran, so this signal takes the default action and kills us.
        process.kill(process.pid, signal);
      });
  }
}
