-- 005_fee_models.sql — populate venues.fee_model.
--
-- (004 is intentionally unused: it briefly held an HNSW index migration that
-- was moved into the Python matcher instead, because a failed index build in
-- corridord's boot-time goose run would have stopped ingestion. Reusing the
-- number would let goose skip this migration on any database that recorded
-- 004 as applied, so the gap stays.)
--
-- WHY this matters more than it looks: venues.fee_model ships seeded as '{}'.
-- The spread engine's whole job is deciding whether YES@A + NO@B < 1.00 NET
-- of fees, and on a typical cross-venue pair the fees are the same order of
-- magnitude as the edge itself — a 1-2 cent gross edge against ~3 cents of
-- combined fees. An empty fee model would therefore turn every thin spread
-- into a phantom arbitrage. internal/spread.ParseFeeModel refuses to treat a
-- missing coefficient as free for exactly this reason; this migration is what
-- makes the engine able to run at all.
--
-- The shape: both venues independently landed on the same formula,
--     fee = contracts x coefficient x price x (1 - price)
-- which peaks at price 0.50 (a coin-flip is the most expensive contract to
-- trade) and decays to nothing at the extremes. Only the coefficient and the
-- rounding differ, so only those are stored.
--
-- WHY the coefficient is a JSON STRING, not a JSON number: Go's encoding/json
-- decodes numbers into float64, which would put a binary-approximated value
-- at the centre of the fee math and break the never-float hard rule at the
-- first parse. Text goes straight into big.Rat losslessly.

-- +goose Up

-- Kalshi: taker fee = ceil(0.07 x C x P x (1-P)), rounded up to the next
-- whole cent PER ORDER. Peak is $1.75 per 100 contracts at 50c.
-- Maker is a separate, lower rate and is deliberately NOT modelled: taking an
-- arb means crossing the spread, so the taker rate is the one that applies.
-- Assuming the maker rate would understate cost and manufacture fake edges.
UPDATE venues
SET fee_model = '{"taker_coefficient": "0.07", "round_up_to_cent": true}'::jsonb
WHERE slug = 'kalshi';

-- Polymarket: same formula, but the coefficient is CATEGORY-DEPENDENT —
-- reported as ~0.04 (politics, finance, tech), ~0.05 (sports, economics,
-- culture, weather, other) and ~0.07 (crypto). Makers pay zero.
--
-- WHY 0.07 (the worst case) rather than a per-category model: markets has no
-- category column, and events.category is nullable and currently unpopulated,
-- so the engine cannot tell which rate applies to a given market. Picking the
-- HIGHEST coefficient understates the edge, which can only ever hide a real
-- arb — never invent one. That is the same "when uncertain, always demote"
-- rule the matcher already applies to confidence tiers, and it is the correct
-- direction to be wrong in. Revisit with per-category rates once markets
-- carries a category worth trusting.
UPDATE venues
SET fee_model = '{"taker_coefficient": "0.07", "round_up_to_cent": false}'::jsonb
WHERE slug = 'polymarket';

-- +goose Down
UPDATE venues SET fee_model = '{}'::jsonb WHERE slug IN ('kalshi', 'polymarket');
