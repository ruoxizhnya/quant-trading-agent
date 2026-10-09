package factor

import (
	"math"
	"sort"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/data/source"
)

// SectorRow is a normalized sector snapshot: change_pct on a given
// trade date for a sector (industry or concept).
type SectorRow struct {
	SectorCode string
	SectorName string
	TradeTime  time.Time
	ChangePct  float64
	LeadingSym string
}

// SectorRowsFromPoints projects sectors UnifiedDataPoints into typed
// rows. The eastmoney_sectors adapter emits DataTypeSectors records
// with these fields.
func SectorRowsFromPoints(points []source.UnifiedDataPoint) []SectorRow {
	out := make([]SectorRow, 0, len(points))
	for _, p := range points {
		if p.DataType != source.DataTypeSectors {
			continue
		}
		out = append(out, SectorRow{
			SectorCode: stringField(p.Data, "sector_code"),
			SectorName: stringField(p.Data, "sector_name"),
			TradeTime:  p.TradeTime,
			ChangePct:  floatField(p.Data, "change_pct"),
			LeadingSym: stringField(p.Data, "leading_symbol"),
		})
	}
	return out
}

// SectorRotationFactor scores each stock by the change_pct of the
// sector it currently belongs to as of tradeDate. Stocks in leading
// sectors get a high factor value, lagging sectors a low one. This is
// a *cross-section* factor: ranking stocks on a single day, not a
// time-series alpha.
//
// As-of semantics: rows with TradeTime > tradeDate are ignored (point-
// in-time safety). When a sector has no row on or before tradeDate,
// the result for stocks in that sector is 0 (neutral) — they are
// excluded from the cross-section ranking rather than inflated by
// forward-looking data.
//
// NaN/Inf handling: rows with non-finite ChangePct are dropped (they
// would propagate into the IC pipeline and void the entire
// cross-section). CR-53 (ODR-012) regression test pins this behaviour.
//
// stockToSector maps symbol → sector_code. Missing stocks are simply
// absent from the output (no signal). Missing sectors default to 0
// (neutral) so a stock that can't be classified doesn't blow up the
// cross-section.
//
// The factor value is sectorChangePct in decimal (e.g. 0.025 = +2.5%).
// To get a 0-1 normalized score, divide by max-abs-value in your
// downstream code; keeping the raw value preserves interpretability
// when feeding into the IC pipeline.
func SectorRotationFactor(rows []SectorRow, tradeDate time.Time, stockToSector map[string]string) map[string]float64 {
	// For each sector, pick the LATEST row with TradeTime <= tradeDate
	// and a finite ChangePct. NaN/Inf rows are dropped — they have
	// no meaningful rank in the cross-section and would pollute
	// downstream IC. (CR-53 ODR-012)
	byCode := make(map[string]SectorRow, len(rows))
	for _, r := range rows {
		if r.TradeTime.After(tradeDate) {
			continue // forward-looking; skip
		}
		if math.IsNaN(r.ChangePct) || math.IsInf(r.ChangePct, 0) {
			continue // CR-53: drop non-finite values
		}
		if existing, ok := byCode[r.SectorCode]; !ok || r.TradeTime.After(existing.TradeTime) {
			byCode[r.SectorCode] = r
		}
	}

	out := make(map[string]float64, len(stockToSector))
	for sym, code := range stockToSector {
		sector, ok := byCode[code]
		if !ok {
			out[sym] = 0
			continue
		}
		out[sym] = sector.ChangePct
	}
	return out
}

// TopMomentumSectors returns the top-N sectors by ChangePct on the
// given date. Used by strategies that need a sector-rotation trading
// universe (long top-N, short bottom-N).
//
// NaN/Inf handling: rows with non-finite ChangePct are dropped. CR-53
// (ODR-012) regression test. Without this filter, a single NaN row
// could either crash the sort comparator (NaN < x is always false)
// or end up in the "top N" arbitrarily.
func TopMomentumSectors(rows []SectorRow, n int) []SectorRow {
	// Reduce to the latest snapshot per sector, dropping NaN/Inf.
	byCode := make(map[string]SectorRow, len(rows))
	for _, r := range rows {
		if math.IsNaN(r.ChangePct) || math.IsInf(r.ChangePct, 0) {
			continue // CR-53: drop non-finite values
		}
		if existing, ok := byCode[r.SectorCode]; !ok || r.TradeTime.After(existing.TradeTime) {
			byCode[r.SectorCode] = r
		}
	}
	out := make([]SectorRow, 0, len(byCode))
	for _, r := range byCode {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ChangePct > out[j].ChangePct
	})
	if n > 0 && n < len(out) {
		out = out[:n]
	}
	return out
}

// IsSectorRowValid mirrors IsCapitalFlowRowValid for sector data.
func IsSectorRowValid(r SectorRow) bool {
	if r.SectorCode == "" && r.SectorName == "" {
		return false
	}
	if r.TradeTime.IsZero() {
		return false
	}
	if math.IsNaN(r.ChangePct) || math.IsInf(r.ChangePct, 0) {
		return false
	}
	return true
}
