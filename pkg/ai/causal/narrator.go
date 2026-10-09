// Package causal 是验证器链「因果维」（P2-9f）的语言模型适配器。
//
// 它只做一件事：把一次提案讲成一个**可被证伪**的理论。验证那件事不在这里
// —— 预测对不对由 pkg/ai/validation 用确定性检验判定，模型没有裁判权。
//
// 分两个包是因为依赖方向：pkg/ai/validation 不认识任何 LLM（它必须能在没有
// 模型的情况下被测试），pkg/ai 那边才是模型客户端所在的地方。
package causal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/validation"
)

// Narrator 用 LLM 讲机制 + 下可证伪的预测。
type Narrator struct {
	client *ai.Client
	model  string
}

// New 构造叙述者。client 未配置（无 API key）时 Narrate 会直接失败 ——
// 调用方应当据此把因果维记成「未评估」，而不是假装讲过了。
func New(client *ai.Client) *Narrator {
	return &Narrator{client: client}
}

// Narrate 让模型讲出机制和可证伪的预测。
//
// 请求里**刻意不含回测结果**（validation 那边已经把它剜掉了）。模型只知道
// 这是个什么策略，不知道它跑得怎么样 —— 只有这样才能逼它下注，而不是照着
// 结果编故事。
func (n *Narrator) Narrate(ctx context.Context, req validation.CausalRequest) (*validation.CausalTheory, error) {
	if n == nil || n.client == nil || !n.client.IsConfigured() {
		return nil, fmt.Errorf("LLM 未配置，因果维无法评估")
	}

	resp, err := n.client.Chat(ctx, []ai.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: buildUserPrompt(req)},
	})
	if err != nil {
		return nil, fmt.Errorf("因果叙述失败: %w", err)
	}

	theory, err := parseTheory(resp)
	if err != nil {
		return nil, err
	}
	return theory, nil
}

const systemPrompt = `你是量化研究的因果审查员。你的任务不是评价策略好坏，而是回答一个问题：
如果这个策略真的有赚钱的机理，那么我们在回测数据里**应该观察到什么**？

铁律：
1. 你**看不到**回测结果，也不需要猜它跑得如何。只会给你策略是什么、参数是什么。
2. 你必须给出可以被数据打脸的预测，每条都要有数值边界。没有边界的陈述（"效果不错"）等于没说。
3. 不要给恒真的预测（"胜率大于 0"）。预测要具体到有可能错。
4. 只输出 JSON，不要解释、不要 markdown 代码块。

输出格式：
{
  "mechanism": "一句话说清为什么这个机理能赚钱（谁在什么时候因为什么愿意用更差的价格成交）",
  "predictions": [
    {"kind": "win_rate", "statement": "人话：应观察到什么", "min": 0.45},
    {"kind": "holding_days", "statement": "持有期应落在几周到几个月", "min": 5, "max": 60}
  ]
}

可用的 kind 与单位（只能用这些，其余的没法验证）：
- win_rate            胜率，0~1
- trade_count         成交笔数，整数
- holding_days        平均持有天数
- max_drawdown        最大回撤幅度，0~1（例如 0.2 = 回撤 20%）
- return_concentration 收益最高的若干天贡献了总收益的多大比例，0~1（可选 top_k，默认 5）

给 2~4 条预测。机理讲不清的策略，预测也一定含糊 —— 那就如实写含糊的预测，
不要为了让结论好看而放宽边界。`

func buildUserPrompt(req validation.CausalRequest) string {
	var b strings.Builder
	b.WriteString("策略：")
	if req.Name != "" {
		b.WriteString(req.Name)
	} else {
		b.WriteString("（未命名）")
	}
	b.WriteString("\n")
	if req.IntentType != "" {
		fmt.Fprintf(&b, "类型：%s\n", req.IntentType)
	}
	if req.Hypothesis != "" {
		fmt.Fprintf(&b, "假设：%s\n", req.Hypothesis)
	}
	if len(req.Params) > 0 {
		params, err := json.Marshal(req.Params)
		if err == nil {
			fmt.Fprintf(&b, "参数：%s\n", params)
		}
	}
	if req.UniverseSize > 0 {
		fmt.Fprintf(&b, "股票池规模：%d\n", req.UniverseSize)
	}
	if req.Periods > 0 {
		fmt.Fprintf(&b, "回测长度：约 %d 个交易日\n", req.Periods)
	}
	b.WriteString("\n请给出机理和可证伪的预测（只输出 JSON）。")
	return b.String()
}

// parseTheory 解析模型输出。
//
// 模型经常在 JSON 外面裹一层 ```json 标记或几句客气话，这里做宽松提取 ——
// 但**不宽松到会误读内容**：提取失败就报错，绝不返回一个"看起来正常"的空
// 理论（那会被当成"讲了但没下注"，同样误导）。
func parseTheory(resp string) (*validation.CausalTheory, error) {
	s := strings.TrimSpace(resp)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 {
		s = s[:i+1]
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")

	var t validation.CausalTheory
	if err := json.Unmarshal([]byte(s), &t); err != nil {
		return nil, fmt.Errorf("因果叙述的 JSON 解析失败（原文前 200 字：%s）: %w",
			truncate(resp, 200), err)
	}
	// 机制一条、预测一条都没有 = 模型没答到点上，按失败处理。
	if strings.TrimSpace(t.Mechanism) == "" && len(t.Predictions) == 0 {
		return nil, fmt.Errorf("模型没有给出机制和预测（原文前 200 字：%s）", truncate(resp, 200))
	}
	return &t, nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
