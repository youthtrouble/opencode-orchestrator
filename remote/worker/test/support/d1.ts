// A test double for the subset of the D1 API the Worker uses, over node:sqlite in memory, with the
// real migrations applied. It is test code we own: the WT16 end-to-end run (real local D1 under
// `wrangler dev`) backs up its fidelity. Like D1, it enforces foreign keys and runs a batch as one
// transaction that rolls back on any error.
import { readdirSync, readFileSync } from "node:fs";
import { DatabaseSync, type SQLInputValue } from "node:sqlite";

const MIGRATIONS_DIR = new URL("../../migrations/", import.meta.url);

type Row = Record<string, unknown>;

interface D1Meta {
  changes: number;
  last_row_id: number;
}

class PreparedStatement {
  readonly #db: DatabaseSync;
  readonly #sql: string;
  readonly #values: SQLInputValue[];

  constructor(db: DatabaseSync, sql: string, values: SQLInputValue[] = []) {
    this.#db = db;
    this.#sql = sql;
    this.#values = values;
  }

  bind(...values: SQLInputValue[]): PreparedStatement {
    return new PreparedStatement(this.#db, this.#sql, values);
  }

  async first(column?: string): Promise<unknown> {
    const row = this.#db.prepare(this.#sql).get(...this.#values) as Row | undefined;
    if (row === undefined) return null;
    return column === undefined ? { ...row } : row[column];
  }

  async all(): Promise<{ success: true; results: Row[]; meta: D1Meta }> {
    return this.execute();
  }

  async run(): Promise<{ success: true; results: Row[]; meta: D1Meta }> {
    return this.execute();
  }

  /**
   * Synchronous, so batch() can run every statement inside one transaction. Known gap: a statement
   * that returns rows (SELECT, or RETURNING) reports `changes: 0`.
   */
  execute(): { success: true; results: Row[]; meta: D1Meta } {
    const statement = this.#db.prepare(this.#sql);
    if (!statement.columns().length) {
      const { changes, lastInsertRowid } = statement.run(...this.#values);
      return { success: true, results: [], meta: { changes: Number(changes), last_row_id: Number(lastInsertRowid) } };
    }
    const results = (statement.all(...this.#values) as Row[]).map((row) => ({ ...row }));
    return { success: true, results, meta: { changes: 0, last_row_id: 0 } };
  }
}

export class D1Double {
  readonly sqlite: DatabaseSync;

  constructor() {
    this.sqlite = new DatabaseSync(":memory:", { enableForeignKeyConstraints: true });
    for (const name of readdirSync(MIGRATIONS_DIR).filter((file) => file.endsWith(".sql")).sort()) {
      this.sqlite.exec(readFileSync(new URL(name, MIGRATIONS_DIR), "utf8"));
    }
  }

  prepare(sql: string): PreparedStatement {
    return new PreparedStatement(this.sqlite, sql);
  }

  async batch(statements: PreparedStatement[]) {
    this.sqlite.exec("BEGIN");
    try {
      const results = statements.map((statement) => statement.execute());
      this.sqlite.exec("COMMIT");
      return results;
    } catch (error) {
      this.sqlite.exec("ROLLBACK");
      throw error;
    }
  }
}
