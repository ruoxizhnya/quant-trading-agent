// OBS-08 切片 1 回归测试（工具层）：闸门失败的文案要带「可用字段（含来源）」，
// 让 AI 能自纠；且 JSON 输出字段结构保持不变。
package builtin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 清退的「假合法」字段：闸门即拒绝，且 error / recommendation 都给出可用字段清单。
func TestValidateFactor_OBS08_PhantomFieldRejectedWithFieldList(t *testing.T) {
	for _, f := range []string{"market_cap", "roe_ttm", "eps"} {
		m := runValidate(t, f)
		assert.Falsef(t, m["valid"].(bool), "%q 改后必须 valid=false", f)
		assert.Falsef(t, m["passed"].(bool), "%q passed 必须为 false", f)

		errStr := m["error"].(string)
		assert.Containsf(t, errStr, f, "%q error 应点名字段", f)
		assert.Containsf(t, errStr, "可用字段", "%q error 应带可用字段清单", f)
		assert.Containsf(t, errStr, "fundamentals:", "%q error 应带来源分组", f)

		rec := m["recommendation"].(string)
		assert.Containsf(t, rec, "可用字段", "%q recommendation 应带可用字段清单", f)
		assert.Containsf(t, rec, "market:", "%q recommendation 应带来源分组", f)
	}
}

// 误拒修复：provider 支持的 ps / roa 现在能过闸门。
func TestValidateFactor_OBS08_PreviouslyRejectedFieldsNowValid(t *testing.T) {
	for _, expr := range []string{"ps", "roa", "revenue", "profit"} {
		m := runValidate(t, expr)
		assert.Truef(t, m["valid"].(bool), "%q 改后应 valid=true, error=%v", expr, m["error"])
		assert.Emptyf(t, m["error"].(string), "%q error 应为空", expr)
		// 合法路径的 recommendation 不含字段清单（只失败路径附）。
		assert.NotEmptyf(t, m["recommendation"].(string), "%q 应有 recommendation", expr)
	}
}

// 契约守护：改了文案也不能动 JSON 输出字段结构（8 字段不变）。
func TestValidateFactor_OBS08_KeepsOutputShape(t *testing.T) {
	m := runValidate(t, "market_cap")
	for _, k := range []string{"valid", "inputs", "ast", "error", "level", "passed", "reason", "recommendation"} {
		_, ok := m[k]
		assert.Truef(t, ok, "输出缺字段 %q", k)
	}
	assert.Len(t, m, 8, "输出字段数应为 8（JSON 契约不变）")
}
