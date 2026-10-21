// D1 allows a limited number of queries per Worker invocation, and every statement counts, batched or
// not (developers.cloudflare.com/d1/platform/limits). D1 has no API that reports how many an invocation
// has used, so the Worker counts the queries it issues itself: every handler gets the database through
// `countingDb`, and work that must not be cut off half-way (the notifier's claim and its final write)
// checks `remaining()` before it starts.
//
// The Worker cannot tell which plan it runs on, so it assumes the Free plan's limit; on the Paid plan
// (1000) that only makes such work wait for a smaller push.
export const QUERIES_PER_INVOCATION = 50;

export class QueryCounter {
  #used = 0;

  get used(): number {
    return this.#used;
  }

  remaining(): number {
    return QUERIES_PER_INVOCATION - this.#used;
  }

  count(queries: number): void {
    this.#used += queries;
  }
}

/** The part of D1 the handlers use; anything else would bypass the count. */
export type CountedDb = Pick<D1Database, "prepare" | "batch">;

class CountedStatement implements D1PreparedStatement {
  readonly inner: D1PreparedStatement;
  readonly #counter: QueryCounter;

  constructor(inner: D1PreparedStatement, counter: QueryCounter) {
    this.inner = inner;
    this.#counter = counter;
  }

  bind(...values: unknown[]): CountedStatement {
    return new CountedStatement(this.inner.bind(...values), this.#counter);
  }

  first<T>(colName?: string): Promise<T | null> {
    this.#counter.count(1);
    return colName === undefined ? this.inner.first<T>() : this.inner.first<T>(colName);
  }

  run<T>(): Promise<D1Result<T>> {
    this.#counter.count(1);
    return this.inner.run<T>();
  }

  all<T>(): Promise<D1Result<T>> {
    this.#counter.count(1);
    return this.inner.all<T>();
  }

  raw<T = unknown[]>(options: { columnNames: true }): Promise<[string[], ...T[]]>;
  raw<T = unknown[]>(options?: { columnNames?: false }): Promise<T[]>;
  raw<T>(options?: { columnNames?: boolean }): Promise<[string[], ...T[]] | T[]> {
    this.#counter.count(1);
    return options?.columnNames ? this.inner.raw<T>({ columnNames: true }) : this.inner.raw<T>();
  }
}

/** Counts each statement `db` runs against `counter`; a batch counts one per statement in it. */
export function countingDb(db: D1Database, counter: QueryCounter): CountedDb {
  return {
    prepare: (query) => new CountedStatement(db.prepare(query), counter),
    batch: (statements) => {
      counter.count(statements.length);
      return db.batch(statements.map(innerStatement));
    },
  };
}

function innerStatement(statement: D1PreparedStatement): D1PreparedStatement {
  if (!(statement instanceof CountedStatement)) throw new TypeError("a batch takes statements from countingDb only");
  return statement.inner;
}
