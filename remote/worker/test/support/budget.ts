// D1's per-invocation query limit, which the in-memory double does not have: every statement counts,
// batched or not (developers.cloudflare.com/d1/platform/limits, 50 on the Workers Free plan). Wrap the
// double in `budgeted` and call `startInvocation` per Worker invocation; a statement over the limit
// throws as D1 does. A batch that would go over is refused whole, before any of it runs (an assumption
// about D1: the docs do not say whether a batch fails part-way through; refusing it whole matches a
// batch being one transaction). It counts on its own, apart from the Worker's QueryCounter, so a test
// at the limit catches a Worker that undercounts.
import type { D1Double } from "./d1.ts";

export class QueryBudget {
  readonly limit: number;
  used = 0;
  /** The most queries any one invocation ran. */
  peak = 0;

  constructor(limit: number) {
    this.limit = limit;
  }

  startInvocation(): void {
    this.used = 0;
  }

  take(queries: number): void {
    if (this.used + queries > this.limit) {
      throw new Error("D1_ERROR: Too many API requests by single worker invocation.");
    }
    this.used += queries;
    this.peak = Math.max(this.peak, this.used);
  }
}

type Statement = ReturnType<D1Double["prepare"]>;

class BudgetedStatement {
  readonly inner: Statement;
  readonly #budget: QueryBudget;

  constructor(inner: Statement, budget: QueryBudget) {
    this.inner = inner;
    this.#budget = budget;
  }

  bind(...values: Parameters<Statement["bind"]>): BudgetedStatement {
    return new BudgetedStatement(this.inner.bind(...values), this.#budget);
  }

  first(column?: string) {
    this.#budget.take(1);
    return this.inner.first(column);
  }

  all() {
    this.#budget.take(1);
    return this.inner.all();
  }

  run() {
    this.#budget.take(1);
    return this.inner.run();
  }
}

export function budgeted(db: D1Double, budget: QueryBudget) {
  return {
    prepare: (sql: string) => new BudgetedStatement(db.prepare(sql), budget),
    batch(statements: BudgetedStatement[]) {
      budget.take(statements.length);
      return db.batch(statements.map((statement) => statement.inner));
    },
  };
}
