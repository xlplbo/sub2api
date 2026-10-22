ALTER TABLE composite_model_routes
    ADD COLUMN IF NOT EXISTS request_condition VARCHAR(20) NOT NULL DEFAULT 'any'
    CHECK (request_condition IN ('any', 'has_image'));

DROP INDEX IF EXISTS idx_composite_model_routes_unique_active;
CREATE UNIQUE INDEX idx_composite_model_routes_unique_active
    ON composite_model_routes (group_id, endpoint, match_type, public_model, request_condition)
    WHERE deleted_at IS NULL;
