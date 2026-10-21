-- What a question asks of the owner: NULL for a decision (every question pushed before WT25), 'action'
-- for steps the owner must do, which carry no recommendation and so offer no Accept.
ALTER TABLE questions ADD COLUMN ask TEXT;
