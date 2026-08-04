CREATE TABLE consolidation.reconciliation_run (
    merchant_id UUID NOT NULL,
    business_date DATE NOT NULL,
    source_position_cut BIGINT NOT NULL CHECK (source_position_cut >= 0),
    missing_entries BIGINT NOT NULL CHECK (missing_entries >= 0),
    extra_entries BIGINT NOT NULL CHECK (extra_entries >= 0),
    duplicated_entries BIGINT NOT NULL CHECK (duplicated_entries >= 0),
    financial_difference_minor BIGINT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    duration_ms BIGINT NOT NULL CHECK (duration_ms >= 0),
    PRIMARY KEY (merchant_id, business_date)
);

-- Note: We store only the latest cut per merchant/date. The compare-and-set logic
-- will be handled in the code (UPDATE ... WHERE source_position_cut < $1).
