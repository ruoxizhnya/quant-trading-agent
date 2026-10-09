// OBS-06 回归测试（工具层根因现场）。
//
// validate_factor 此前 Parse 成功即 valid=true，不校验算子名 —— 于是
// `CROSS(MA(close,5), MA(close,20))`（两个算子都不存在）被判「合法」。
// 本测试直接调 ValidateFactorTool.Execute 钉住修复。
package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runValidate(t *testing.T, expr string) map[string]interface{} {
	t.Helper()
	res, err := NewValidateFactorTool().Execute(context.Background(), map[string]interface{}{
		"expression": expr,
	})
	require.NoError(t, err, "闸门不通过不应返回 Go error（是 valid=false 的校验结果）")
	m, ok := res.(map[string]interface{})
	require.True(t, ok, "结果应是 map[string]interface{}, got %T", res)
	return m
}

// OBS-06 正证据 + 反证腿。
func TestValidateFactor_OBS06_UnknownOperatorsRejected(t *testing.T) {
	// 正证据（改前 valid=true 的假合法现场）。
	bad := []struct {
		expr    string
		mustSay string
	}{
		{"CROSS(MA(close, 5), MA(close, 20))", "CROSS"},
		{"ts_mean(close)", "ts_mean"},      // 缺参数
		{"nosuchfield + 1", "nosuchfield"}, // 未知字段
	}
	for _, tc := range bad {
		m := runValidate(t, tc.expr)
		assert.Falsef(t, m["valid"].(bool), "改后 %q 必须 valid=false", tc.expr)
		assert.Falsef(t, m["passed"].(bool), "%q passed 必须为 false", tc.expr)
		assert.Equal(t, GateReasonSyntaxError, m["reason"], "%q reason", tc.expr)
		errStr := m["error"].(string)
		assert.NotEmptyf(t, errStr, "%q 必须带 error 原因", tc.expr)
		assert.Containsf(t, errStr, tc.mustSay, "%q error 应含 %q", tc.expr, tc.mustSay)
		assert.NotEmptyf(t, m["recommendation"].(string), "%q 必须有 recommendation", tc.expr)
	}

	// 反证腿：合法表达式不能被我拦掉。
	good := []string{
		"cs_rank(ts_pct_change(close, 20)) > 0.8",
		"ts_rank(close, 20)",
		"(close - ts_mean(close, 20)) / ts_std(close, 20)",
		"ts_rma(close, 14) > 0",
		"ts_ewma(close, 0.3)",
		"ts_kalman(close, 0.1, 0.5)",
	}
	for _, expr := range good {
		m := runValidate(t, expr)
		assert.Truef(t, m["valid"].(bool), "反证腿：%q 应仍 valid=true，error=%v", expr, m["error"])
		assert.Emptyf(t, m["error"].(string), "%q error 应为空", expr)
	}
}

// 闸门失败时保持既有输出字段结构不变（8 字段）。
func TestValidateFactor_GateFailure_KeepsOutputShape(t *testing.T) {
	m := runValidate(t, "CROSS(MA(close, 5), MA(close, 20))")

	for _, k := range []string{"valid", "inputs", "ast", "error", "level", "passed", "reason", "recommendation"} {
		_, ok := m[k]
		assert.Truef(t, ok, "输出缺字段 %q", k)
	}
	assert.Len(t, m, 8, "输出字段数应为 8（JSON 契约不变）")
	assert.Equal(t, "L1", m["level"])
	// 无效时 inputs/ast 为空（与 parse-failure 路径契约一致）。
	assert.Empty(t, m["inputs"].([]string))
	assert.Empty(t, m["ast"].(string))
}
