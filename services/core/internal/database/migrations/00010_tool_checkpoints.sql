-- +goose Up
ALTER TABLE channel.tasks DROP CONSTRAINT tasks_state_check;
ALTER TABLE channel.tasks ADD CONSTRAINT tasks_state_check CHECK(state IN ('pending','claimed','model_running','delivery_pending','delivery_running','succeeded','failed','unknown','waiting_approval','rejected','expired','tool_pending','tool_running'));
ALTER TABLE channel.tasks DROP CONSTRAINT task_lease_shape;
ALTER TABLE channel.tasks ADD CONSTRAINT task_lease_shape CHECK (
 (state IN ('claimed','model_running','delivery_pending','delivery_running','tool_pending','tool_running') AND lease_owner IS NOT NULL AND lease_until IS NOT NULL)
 OR (state IN ('pending','succeeded','failed','unknown','waiting_approval','rejected','expired') AND lease_owner IS NULL AND lease_until IS NULL)
);
ALTER TABLE channel.tasks ADD COLUMN tool_history jsonb NOT NULL DEFAULT '[]';
ALTER TABLE channel.tasks ADD COLUMN tool_invocation jsonb;
ALTER TABLE channel.tasks ADD COLUMN tool_steps integer NOT NULL DEFAULT 0 CHECK(tool_steps BETWEEN 0 AND 4);
ALTER TABLE channel.tool_operations ADD COLUMN result jsonb;
