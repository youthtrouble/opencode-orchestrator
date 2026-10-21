CREATE TABLE machines (
  id TEXT PRIMARY KEY, orch_version TEXT NOT NULL,
  first_push_at TEXT, last_push_at TEXT, last_pull_at TEXT, budget_json TEXT);
CREATE TABLE projects (
  key TEXT PRIMARY KEY, machine_id TEXT NOT NULL REFERENCES machines(id),
  name TEXT NOT NULL, hidden INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL CHECK (state IN ('working','needs_you','paused','failed','done','idle')),
  snapshot_json TEXT NOT NULL, snapshot_hash TEXT NOT NULL,
  sent_at TEXT NOT NULL, received_at TEXT NOT NULL, state_since TEXT NOT NULL,
  notified_state TEXT);
CREATE TABLE questions (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL REFERENCES projects(key),
  asked_at TEXT NOT NULL, asked_by TEXT NOT NULL, subject TEXT NOT NULL, body TEXT NOT NULL,
  recommended TEXT, blocking INTEGER NOT NULL,
  first_seen_at TEXT NOT NULL, closed_at TEXT,
  notify_state TEXT NOT NULL DEFAULT 'pending' CHECK (notify_state IN ('pending','sent','suppressed','failed')));
CREATE INDEX questions_open_by_project ON questions(project_key) WHERE closed_at IS NULL;
CREATE TABLE answers (
  id TEXT PRIMARY KEY, question_id TEXT NOT NULL REFERENCES questions(id),
  kind TEXT NOT NULL CHECK (kind IN ('accept','typed')), text TEXT NOT NULL,
  client_key TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, apply_after TEXT NOT NULL,
  cancelled_at TEXT, delivered_at TEXT,
  outcome TEXT CHECK (outcome IN ('applied','superseded','rejected')), outcome_detail TEXT, settled_at TEXT);
-- Exactly-once: at most one live (not undone) answer per question.
CREATE UNIQUE INDEX answers_one_live_per_question ON answers(question_id) WHERE cancelled_at IS NULL;
CREATE TABLE actions (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL REFERENCES projects(key),
  kind TEXT NOT NULL CHECK (kind IN ('pause','resume')),
  client_key TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, apply_after TEXT NOT NULL,
  cancelled_at TEXT, delivered_at TEXT,
  outcome TEXT CHECK (outcome IN ('applied','superseded','rejected')), outcome_detail TEXT, settled_at TEXT);
CREATE UNIQUE INDEX actions_one_open_per_project ON actions(project_key) WHERE cancelled_at IS NULL AND settled_at IS NULL;
CREATE TABLE notify_log (day TEXT PRIMARY KEY, sent INTEGER NOT NULL);
