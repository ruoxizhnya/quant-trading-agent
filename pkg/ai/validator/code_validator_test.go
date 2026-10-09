package validator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P0-6：ValidateCompilation 此前用 `go tool compile -V=full` —— 那是
// **打印编译器版本号**的开关，根本不读后面的文件。于是只要 gofmt 过得
// 去，result.Compiles 就恒为 true：一个字段名在撒谎，调用方以为代码
// 编译过了。
//
// 下面这组测试锁的契约：Compiles 必须反映真实的编译结果，gofmt 抓不到
// 的类型错误也要抓得到。

// typeErrorButValidSyntax 语法完全正确（gofmt 无话可说），但引用了不
// 存在的函数 —— 只有真的做类型检查才能发现。
const typeErrorButValidSyntax = `package strategy

func Compute() float64 {
	return undefinedFunction()
}
`

func TestValidateCompilation_CatchesTypeErrors(t *testing.T) {
	v := NewCodeValidator()
	res := v.ValidateCompilation(typeErrorButValidSyntax)

	require.NotNil(t, res)
	assert.True(t, res.SyntaxValid, "语法确实没问题，能走到编译这一步")
	assert.False(t, res.Compiles, "类型错误必须被抓到 —— 这是 -V=full 漏掉的那类错误")
	assert.NotEmpty(t, res.Errors)
}

func TestValidateCompilation_AcceptsValidCode(t *testing.T) {
	v := NewCodeValidator()
	res := v.ValidateCompilation(`package strategy

func Compute() float64 {
	return 1.0
}
`)

	require.NotNil(t, res)
	assert.True(t, res.Compiles, "合法代码不该被误判")
	assert.Empty(t, res.Errors)
}

func TestValidateCompilation_CanResolveProjectImports(t *testing.T) {
	// 生成的策略代码会 import 项目内的包，编译必须在项目根下进行，
	// 否则连合法代码都会因为找不到包而失败。
	v := NewCodeValidator()
	res := v.ValidateCompilation(`package strategy

import "github.com/ruoxizhnya/quant-trading/pkg/domain"

func Compute(bar domain.OHLCV) float64 {
	return bar.Close
}
`)

	require.NotNil(t, res)
	assert.True(t, res.Compiles, "项目内的 import 必须能解析")
	assert.Empty(t, res.Errors)
}

func TestValidateCompilation_SyntaxError(t *testing.T) {
	v := NewCodeValidator()
	res := v.ValidateCompilation("package strategy\nfunc broken( {\n")

	require.NotNil(t, res)
	assert.False(t, res.SyntaxValid)
	assert.False(t, res.Compiles)
	assert.NotEmpty(t, res.Errors)
}
