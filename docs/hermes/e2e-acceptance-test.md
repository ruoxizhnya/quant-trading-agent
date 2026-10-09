# E2E Acceptance Test — Hermes Autonomous Factor Mining

> **Phase**: 2.6 (Hermes Agent Integration)
> **Status**: Acceptance test procedure (requires live infrastructure)
> **Related**: System Design §11 Phase 2 acceptance criteria
> 设计文档已于 2026-09-18 补齐（TASKS P2-11）：见
> [system-design.md](system-design.md)。注意它是**从代码反推**的 ——
> 原始文档随 `.trae/documents/` 永久遗失，只复原了代码实际引用的
> §3.2 与 §6.2 两节。
> **Prerequisite**: Phase 2.7 Go integration tests passing (`handlers_tools_integration_test.go`)

---

## Acceptance Criteria

> **Source**: System Design §11 Phase 2 验收

> 给定目标"挖掘 IC>0.04 的动量因子"，Hermes 在 **$2 预算**内自主完成
> L1-L4 验证并保存到基因池。

### Concrete Success Conditions

| # | Condition | Verification |
|---|-----------|-------------|
| 1 | Hermes discovers all 19 tools via `GET /api/tools` | `curl` returns `count: 19` |
| 2 | Hermes calls `list_factors` to avoid duplicates | trajectory log shows `list_factors` call |
| 3 | Hermes calls `get_market_regime` for market context | trajectory log shows `get_market_regime` call |
| 4 | Hermes generates a valid DSL expression (L1 passes) | `validate_factor` returns `passed: true` |
| 5 | Hermes calls `compute_factor_ic` (L2) with IC ≥ 0.04 | L2 result shows `ic ≥ 0.04` |
| 6 | Hermes calls `backtest.run` (L3) + `summarize_backtest` | L3 result shows `sharpe ≥ 0.5` |
| 7 | Hermes calls `walk_forward_validate` (L4) | L4 result shows `degradation ≥ 0.50` |
| 8 | Hermes calls `save_factor` + `save_strategy` | gene pool has new entries |
| 9 | Hermes calls `get_strategy_lineage` for reflection | trajectory log shows lineage call |
| 10 | Total cost < $2 (or iterations < 20) | budget controller reports `cost < 2.0` |
| 11 | Hermes returns structured result | `status: "success"` with `factor_id` + `strategy_id` |

---

## Prerequisites

### Infrastructure

| Component | Port | Verification |
|-----------|------|-------------|
| Ollama (hermes-3:8b) | :11434 | `curl http://localhost:11434/api/tags` lists `hermes-3:8b` |
| Hermes Agent | — | `~/.hermes/` directory exists with config/skills/prompts |
| analysis-service | :8085 | `curl http://localhost:8085/api/tools` returns `count: 19` |
| data-service | :8081 | `curl http://localhost:8081/health` returns 200 |
| PostgreSQL | :5432 | `psql` can connect; `gene_pool_factors` table exists |
| Redis | :6379 | `redis-cli ping` returns `PONG` |

### Data Requirements

- OHLCV data for CSI 300 stocks covering `2020-01-01` to `2024-12-31`
- At least 200 trading days for `000300.SH` (needed by `get_market_regime`)
- `gene_pool_factors` and `gene_pool_strategies` tables exist (can be empty)

### Hermes Deployment

```bash
# 1. Install Ollama + pull model
ollama pull hermes-3:8b

# 2. Deploy Hermes config files
mkdir -p ~/.hermes/{config,skills,prompts,tools,logs}
cp docs/hermes/config/hermes.yaml       ~/.hermes/config/hermes.yaml
cp docs/hermes/skills/autonomous_factor_mining.md ~/.hermes/skills/autonomous_factor_mining.md
cp docs/hermes/prompts/quant-research.md ~/.hermes/prompts/quant-research.md
cp docs/hermes/tools-quant-backtest.yaml ~/.hermes/tools/quant-backtest.yaml

# 3. Verify Hermes can discover tools
# (Hermes CLI depends on the framework — see Hermes docs for invocation)
# Expected: Hermes reports 19 tools discovered from http://localhost:8085/api/tools
```

---

## Test Procedure

### Step 1: Smoke Test — Tool Discovery

Before running the full autonomous loop, verify the MCP bridge works:

```bash
# Verify all 19 tools are discoverable
curl -s http://localhost:8085/api/tools | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(f'Tool count: {data[\"count\"]}')
assert data['count'] == 19, f'Expected 19 tools, got {data["count"]}'
for t in data['tools']:
    print(f'  - {t[\"name\"]}: {t[\"description\"][:60]}...')
print('✅ Tool discovery OK')
"
```

### Step 2: Smoke Test — L1 Gate

```bash
# Verify validate_factor works (L1 gate)
curl -s -X POST http://localhost:8085/api/tools/validate_factor \
  -H "Content-Type: application/json" \
  -d '{"args": {"expression": "ts_rank(close, 20)"}}' | python3 -c "
import json, sys
r = json.load(sys.stdin)['result']
print(f'valid: {r[\"valid\"]}')
print(f'level: {r[\"level\"]}')
print(f'passed: {r[\"passed\"]}')
print(f'reason: {r[\"reason\"]}')
assert r['valid'] == True
assert r['passed'] == True
assert r['level'] == 'L1'
print('✅ L1 gate OK')
"
```

### Step 3: Smoke Test — Market Regime

```bash
# Verify get_market_regime works (needs 200+ bars for 000300.SH)
curl -s -X POST http://localhost:8085/api/tools/get_market_regime \
  -H "Content-Type: application/json" \
  -d '{"args": {"start_date": "2023-01-01", "end_date": "2024-01-01"}}' | python3 -c "
import json, sys
r = json.load(sys.stdin)['result']
print(f'symbol: {r[\"symbol\"]}')
print(f'bars_analyzed: {r[\"bars_analyzed\"]}')
print(f'trend: {r[\"trend\"]}')
print(f'volatility: {r[\"volatility\"]}')
print(f'sentiment: {r[\"sentiment\"]}')
assert r['bars_analyzed'] > 0
assert r['trend'] in ('bull', 'bear', 'sideways')
print('✅ Market regime OK')
"
```

### Step 4: Full Autonomous Loop (Hermes-driven)

Invoke the Hermes agent with the autonomous_factor_mining skill:

```
# Hermes invocation (syntax depends on Hermes framework version)
hermes run skill=autonomous_factor_mining \
  target_ic=0.04 \
  category=momentum \
  budget=2.0 \
  max_iterations=20 \
  start_date=2022-01-01 \
  end_date=2024-01-01
```

Or via natural language prompt:

```
用户: "挖掘一个 IC > 0.04 的动量因子，预算 $2，最多 20 次迭代"
```

### Step 5: Verify Results

After Hermes completes, verify:

```bash
# 5a. Check gene pool for new factor
curl -s -X POST http://localhost:8085/api/tools/list_factors \
  -H "Content-Type: application/json" \
  -d '{"args": {"category": "momentum", "limit": 5}}' | python3 -c "
import json, sys
factors = json.load(sys.stdin)['result']
print(f'Found {len(factors)} momentum factors')
for f in factors:
    print(f'  - {f[\"name\"]}: IC={f[\"ic\"]:.4f}, formula={f[\"formula\"]}')
    assert f['ic'] >= 0.04, f'IC {f[\"ic\"]} below 0.04 target'
print('✅ Gene pool has validated factor')
"

# 5b. Check Hermes trajectory log
cat ~/.hermes/logs/agent.log | python3 -c "
import json, sys
calls = [json.loads(l) for l in sys.stdin if l.strip()]
tool_calls = [c for c in calls if c.get('type') == 'tool_call']
tool_names = [c['tool'] for c in tool_calls]
print(f'Total tool calls: {len(tool_calls)}')
print(f'Tools used: {sorted(set(tool_names))}')

# Verify all expected tools were called
expected = {'list_factors', 'get_market_regime', 'validate_factor',
            'compute_factor_ic', 'walk_forward_validate', 'save_factor'}
missing = expected - set(tool_names)
assert not missing, f'Missing tool calls: {missing}'
print('✅ All expected tools called')
"

# 5c. Check budget
cat ~/.hermes/logs/agent.log | python3 -c "
import json, sys
calls = [json.loads(l) for l in sys.stdin if l.strip()]
budget = [c for c in calls if c.get('type') == 'budget']
if budget:
    final = budget[-1]
    print(f'Final cost: \${final[\"cost\"]:.4f}')
    print(f'Iterations: {final[\"iterations\"]}')
    print(f'LLM calls: {final[\"llm_calls\"]}')
    assert final['cost'] < 2.0, f'Budget exceeded: \${final[\"cost\"]}'
    print('✅ Within budget')
"
```

---

## Expected Outcome

On success, Hermes should output:

```json
{
  "status": "success",
  "factor_id": "fg_xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "strategy_id": "sg_xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "ic": 0.04,
  "sharpe": 0.6,
  "degradation": 0.75,
  "cost": 1.23,
  "iterations": 8,
  "llm_calls": 45
}
```

### Failure Modes

| Status | Cause | Debugging |
|--------|-------|-----------|
| `budget_exhausted` | Cost reached $2 before finding IC>0.04 factor | Check L2 IC values in trajectory; may need higher budget or different category |
| `iteration_limit` | 20 iterations exhausted without convergence | Check which gates failed; L1 failures suggest DSL syntax issues |
| `convergence_stall` | 5 consecutive rounds with no IC improvement | Try different category or wider stock pool |
| `l4_failed` | All factors had degradation < 0.50 (high overfitting) | Factor may be overfit to in-sample data; try simpler expressions |

---

## Automated Smoke Test (Python)

For CI/CD or quick verification without Hermes, run the Go integration tests:

```bash
# Phase 2.7 Go integration tests — verify tool chain at HTTP level
go test ./cmd/analysis/ -run "TestIntegration_" -v -count=1

# Expected: 6 tests pass
# - TestIntegration_L1Gate_ValidExpression_HTTP
# - TestIntegration_L1Gate_SyntaxError_HTTP
# - TestIntegration_DiscoverySchema_Matches_ValidateFactor
# - TestIntegration_SaveFactor_ListFactors_RoundTrip_HTTP
# - TestIntegration_L1Gate_Then_SaveFactor_Chain_HTTP
# - TestIntegration_GateDecision_AllFields_Survive_HTTP
```

These tests verify the Go-side tool chain (L1 gate → save → list round-trip)
through HTTP with real builtin tools and mock dependencies. They do NOT
test the LLM-driven autonomous loop — that requires the full Hermes E2E
test above.

---

## Troubleshooting

### "get_market_regime returns error: insufficient bars"

The `DetectRegime` function requires at least `SlowMAPeriod` (usually 200)
trading days of OHLCV data. Ensure the date range spans enough trading days:

```bash
# Check available OHLCV data
curl -s "http://localhost:8081/ohlcv/000300.SH?start_date=2022-01-01&end_date=2024-01-01" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(f'Records: {len(data)}')
assert len(data) >= 200, 'Need at least 200 bars for market regime detection'
"
```

### "compute_factor_ic returns IC=0"

The factor evaluation needs sufficient stock data. Verify the stock pool
has data for the evaluation window:

```bash
# Check data availability for a sample stock
curl -s "http://localhost:8081/ohlcv/000001.SZ?start_date=2022-01-01&end_date=2024-01-01" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(f'Records: {len(data)}')
"
```

### "save_factor returns 500 error"

The gene pool tables may not exist. Run migrations:

```bash
# Verify gene_pool_factors table exists
psql -h localhost -U postgres -d quant_trading -c "\d gene_pool_factors"

# If missing, run migrations
# (see migrations/ directory for SQL files)
```

### "Hermes cannot discover tools"

Verify the analysis-service is running and the MCP endpoint is accessible:

```bash
curl -v http://localhost:8085/api/tools 2>&1 | head -20
```

If the endpoint is not responding, check:
1. analysis-service is running (`docker compose ps`)
2. The service is healthy (`curl http://localhost:8085/health`)
3. Port 8085 is not blocked by firewall

---

## Change Log

| Version | Date | Change |
|---------|------|--------|
| 1.0.0 | 2026-07-02 | Initial version (Phase 2.6). Acceptance criteria, prerequisites, test procedure, troubleshooting. |
