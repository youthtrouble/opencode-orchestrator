import test from "node:test";
import assert from "node:assert/strict";
import { D1Double } from "./support/d1.ts";

const T = "2026-10-20T12:00:00.000Z";
const INSERT_PROJECT = `INSERT INTO projects
  (key, machine_id, name, state, snapshot_json, snapshot_hash, sent_at, received_at, state_since)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`;

async function seededQuestion() {
  const db = new D1Double();
  await db.batch([
    db.prepare("INSERT INTO machines (id, orch_version) VALUES (?, ?)").bind("m1", "1.0.0"),
    db.prepare(INSERT_PROJECT).bind("p1", "m1", "demo", "needs_you", "{}", "h", T, T, T),
    db
      .prepare(
        `INSERT INTO questions (id, project_key, asked_at, asked_by, subject, body, blocking, first_seen_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      )
      .bind("q1", "p1", T, "conductor", "subject", "body", 1, T),
  ]);
  return db;
}

function insertAnswer(db: D1Double, id: string, kind = "accept") {
  return db
    .prepare(
      `INSERT INTO answers (id, question_id, kind, text, client_key, created_at, apply_after)
       VALUES (?, ?, ?, ?, ?, ?, ?)`,
    )
    .bind(id, "q1", kind, "yes", `client-${id}`, T, T)
    .run();
}

test("the migration creates every table and partial index", async () => {
  const db = new D1Double();
  const schema = "SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND sql IS NOT NULL ORDER BY name";
  const { results } = await db.prepare(schema).all();
  assert.deepEqual(
    results.map((row) => row.name),
    [
      "actions",
      "actions_one_open_per_project",
      "answers",
      "answers_one_live_per_question",
      "machines",
      "notify_log",
      "projects",
      "questions",
      "questions_open_by_project",
    ],
  );
});

test("a question takes a second answer only after the first is cancelled", async () => {
  const db = await seededQuestion();
  await insertAnswer(db, "a1");
  await assert.rejects(insertAnswer(db, "a2"), /UNIQUE constraint failed: answers.question_id/);
  await db.prepare("UPDATE answers SET cancelled_at = ? WHERE id = ?").bind(T, "a1").run();
  const { meta } = await insertAnswer(db, "a2");
  assert.equal(meta.changes, 1);
});

test("CHECK constraints reject an unknown project state and answer kind", async () => {
  const db = await seededQuestion();
  await assert.rejects(
    db.prepare("UPDATE projects SET state = ? WHERE key = ?").bind("sleeping", "p1").run(),
    /CHECK constraint failed/,
  );
  await assert.rejects(insertAnswer(db, "a1", "shrug"), /CHECK constraint failed/);
});

test("a batch is one transaction: a failing statement rolls back the earlier ones", async () => {
  const db = new D1Double();
  await assert.rejects(
    db.batch([
      db.prepare("INSERT INTO notify_log (day, sent) VALUES (?, ?)").bind("2026-10-20", 1),
      db.prepare("INSERT INTO notify_log (day, sent) VALUES (?, ?)").bind("2026-10-20", 2),
    ]),
    /UNIQUE constraint failed/,
  );
  assert.equal(await db.prepare("SELECT COUNT(*) AS n FROM notify_log").first("n"), 0);
});

test("foreign keys are enforced, as on D1", async () => {
  const db = new D1Double();
  await assert.rejects(
    db.prepare(INSERT_PROJECT).bind("p1", "no-such-machine", "demo", "idle", "{}", "h", T, T, T).run(),
    /FOREIGN KEY constraint failed/,
  );
});
