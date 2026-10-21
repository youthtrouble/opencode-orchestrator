-- The push attempt whose write a project row holds. A push's question writes apply only while the
-- row carries its own id, so a rival push that read the same row in the same millisecond, with the
-- same snapshot hash, cannot pass for the winner (WT8 review R2).
ALTER TABLE projects ADD COLUMN push_id TEXT;
