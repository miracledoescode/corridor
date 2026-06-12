# Corridor Runbook

## Kalshi unreachable / zero quotes

Symptom: `/healthz` shows kalshi `healthy:false` with `last_quote_at:null`
while Polymarket ingests normally — every Kalshi request failing, not a
parse problem.

Checklist, in order:

1. **Host.** `KALSHI_BASE_URL` must point at the keyless public host
   `https://external-api.kalshi.com/trade-api/v2`.
   `api.elections.kalshi.com` is the authenticated trading host (key-signed
   requests); anonymous market-data polls fail there outright. This was the
   root cause of the 2026-06 day-one outage.
2. **Probe from the SAME network corridord runs on** (datacenter IPs can be
   treated differently from residential ones):
   `curl -sS -H "User-Agent: CorridorBot/0.1 (contact: miraclesayscode@gmail.com)" \
      "https://external-api.kalshi.com/trade-api/v2/markets?limit=2"`
   - `200` + JSON → host fine; look at corridord logs for the real error.
   - `403` + Cloudflare/challenge HTML → bot/IP filtering of this network.
     Do NOT add header spoofing, UA rotation, or proxy services — Corridor
     is a polite identified bot and that reputation is an asset. Move the
     workload (US-region / non-datacenter egress) instead.
   - `401` → endpoint now requires auth; re-check Kalshi docs for the
     current public host before doing anything else.
3. **Before choosing a prod region/host**: run the step-2 probe from the
   candidate network for BOTH venues first. Ingestion uptime is the prime
   directive — venue reachability is a deployment criterion, not an
   afterthought.
