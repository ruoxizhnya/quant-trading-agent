package gene_pool

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
)

// MutationType defines the type of mutation operation.
type MutationType string

const (
	MutationChangeWindow   MutationType = "change_window"
	MutationChangeOperator MutationType = "change_operator"
	MutationAddOperator    MutationType = "add_operator"
	MutationRemoveOperator MutationType = "remove_operator"
	MutationChangeField    MutationType = "change_field"
	MutationWrapFunction   MutationType = "wrap_function"
	MutationUnwrapFunction MutationType = "unwrap_function"
)

// Mutator performs mutations on factor formulas.
type Mutator struct {
	rng *rand.Rand
}

// NewMutator creates a new Mutator with the given seed.
func NewMutator(seed int64) *Mutator {
	return &Mutator{rng: rand.New(rand.NewSource(seed))}
}

// Mutate applies a random mutation to a formula.
func (m *Mutator) Mutate(formula string) (string, MutationType, error) {
	if strings.TrimSpace(formula) == "" {
		return "", "", fmt.Errorf("empty formula")
	}

	mutations := []func(string) (string, MutationType, error){
		m.mutateChangeWindow,
		m.mutateChangeOperator,
		m.mutateAddOperator,
		m.mutateRemoveOperator,
		m.mutateChangeField,
		m.mutateWrapFunction,
		m.mutateUnwrapFunction,
	}

	// Try mutations until one succeeds
	for attempts := 0; attempts < 10; attempts++ {
		idx := m.rng.Intn(len(mutations))
		result, mutType, err := mutations[idx](formula)
		if err == nil && result != formula {
			return result, mutType, nil
		}
	}

	return formula, "", fmt.Errorf("no valid mutation found")
}

// MutateN applies n random mutations to a formula.
func (m *Mutator) MutateN(formula string, n int) (string, []MutationType, error) {
	result := formula
	var types []MutationType

	for i := 0; i < n; i++ {
		mutated, mutType, err := m.Mutate(result)
		if err != nil {
			break
		}
		result = mutated
		types = append(types, mutType)
	}

	return result, types, nil
}

// mutateChangeWindow changes a window parameter in a time series function.
func (m *Mutator) mutateChangeWindow(formula string) (string, MutationType, error) {
	// Find all numbers in the formula
	numbers := extractNumbers(formula)
	if len(numbers) == 0 {
		return formula, "", fmt.Errorf("no window parameters found")
	}

	// Pick a random number to change
	targetIdx := m.rng.Intn(len(numbers))
	targetNum := numbers[targetIdx]

	// Generate a new window size (common values: 5, 10, 20, 60, 120, 250)
	commonWindows := []int{5, 10, 20, 60, 120, 250}
	newWindow := commonWindows[m.rng.Intn(len(commonWindows))]

	// Replace the number
	newFormula := replaceNthNumber(formula, targetNum, newWindow, targetIdx)
	if newFormula == formula {
		return formula, "", fmt.Errorf("replacement failed")
	}

	return newFormula, MutationChangeWindow, nil
}

// mutateChangeOperator changes a binary operator.
func (m *Mutator) mutateChangeOperator(formula string) (string, MutationType, error) {
	operators := []string{"+", "-", "*", "/"}
	found := false
	result := formula

	for _, op := range operators {
		if strings.Contains(result, " "+op+" ") || strings.Contains(result, "("+op+")") {
			// Find position and replace with another operator
			newOp := operators[m.rng.Intn(len(operators))]
			if newOp != op {
				result = strings.Replace(result, " "+op+" ", " "+newOp+" ", 1)
				found = true
				break
			}
		}
	}

	if !found {
		return formula, "", fmt.Errorf("no operator found to change")
	}

	return result, MutationChangeOperator, nil
}

// mutateAddOperator adds a binary operator with a literal.
func (m *Mutator) mutateAddOperator(formula string) (string, MutationType, error) {
	operators := []string{"+", "-", "*", "/"}
	op := operators[m.rng.Intn(len(operators))]
	literal := m.rng.Intn(10) + 1

	// Wrap formula with operator
	result := fmt.Sprintf("(%s %s %d)", formula, op, literal)
	return result, MutationAddOperator, nil
}

// mutateRemoveOperator removes an outer binary operator.
func (m *Mutator) mutateRemoveOperator(formula string) (string, MutationType, error) {
	// Check if formula is wrapped in parens with binary op
	if !strings.HasPrefix(formula, "(") || !strings.HasSuffix(formula, ")") {
		return formula, "", fmt.Errorf("no outer operator to remove")
	}

	// Try to find the main operator and return left or right side
	inner := formula[1 : len(formula)-1]
	for _, op := range []string{" + ", " - ", " * ", " / "} {
		if idx := strings.Index(inner, op); idx > 0 {
			// Return left or right side randomly
			if m.rng.Float64() < 0.5 {
				return strings.TrimSpace(inner[:idx]), MutationRemoveOperator, nil
			}
			return strings.TrimSpace(inner[idx+len(op):]), MutationRemoveOperator, nil
		}
	}

	return formula, "", fmt.Errorf("no binary operator found")
}

// mutateChangeField changes a data field (e.g., close → open).
func (m *Mutator) mutateChangeField(formula string) (string, MutationType, error) {
	fields := []string{"close", "open", "high", "low", "volume", "vwap"}

	found := false
	result := formula
	for _, field := range fields {
		if strings.Contains(result, field) {
			newField := fields[m.rng.Intn(len(fields))]
			if newField != field {
				result = strings.Replace(result, field, newField, 1)
				found = true
				break
			}
		}
	}

	if !found {
		return formula, "", fmt.Errorf("no field found to change")
	}

	return result, MutationChangeField, nil
}

// wrapCandidates 是 mutateWrapFunction 可包裹的算子名（一元逐元素算子）。
//
// ─── 为什么提成包级变量（K3b）─────────────────────────────────────
// 原先是函数内的局部 slice，护栏无从验证它 —— 若注册表改名/删名，这里
// 会静默产出「过不了 DSL 闸门」的表达式（K3b 的危害）。提成包级变量后，
// 测试能断言「名单 ⊆ expression.AvailableOperators()」，改名即红。
// 内容与提取前**逐字相同**，行为不变。
var wrapCandidates = []string{"abs", "log", "sqrt", "sign"}

// mutateWrapFunction wraps the formula in a function.
func (m *Mutator) mutateWrapFunction(formula string) (string, MutationType, error) {
	functions := wrapCandidates
	fn := functions[m.rng.Intn(len(functions))]

	result := fmt.Sprintf("%s(%s)", fn, formula)
	return result, MutationWrapFunction, nil
}

// unwrapCandidates 是 mutateUnwrapFunction 能剥掉的外层算子名
// （一元逐元素 + 横截面排名类）。提成包级变量的理由同 wrapCandidates（K3b）。
var unwrapCandidates = []string{"abs", "log", "sqrt", "sign", "cs_rank", "cs_zscore"}

// mutateUnwrapFunction removes an outer function call.
func (m *Mutator) mutateUnwrapFunction(formula string) (string, MutationType, error) {
	functions := unwrapCandidates

	for _, fn := range functions {
		prefix := fn + "("
		if strings.HasPrefix(formula, prefix) && strings.HasSuffix(formula, ")") {
			inner := formula[len(prefix) : len(formula)-1]
			return inner, MutationUnwrapFunction, nil
		}
	}

	return formula, "", fmt.Errorf("no outer function to unwrap")
}

// extractNumbers finds all integers in a formula string.
func extractNumbers(formula string) []int {
	var numbers []int
	var current strings.Builder

	for _, ch := range formula {
		if ch >= '0' && ch <= '9' {
			current.WriteRune(ch)
		} else {
			if current.Len() > 0 {
				if n, err := strconv.Atoi(current.String()); err == nil {
					numbers = append(numbers, n)
				}
				current.Reset()
			}
		}
	}

	if current.Len() > 0 {
		if n, err := strconv.Atoi(current.String()); err == nil {
			numbers = append(numbers, n)
		}
	}

	return numbers
}

// replaceNthNumber replaces the nth occurrence of a number in a formula.
func replaceNthNumber(formula string, oldNum, newNum, n int) string {
	oldStr := strconv.Itoa(oldNum)
	newStr := strconv.Itoa(newNum)

	count := 0
	var result strings.Builder
	i := 0

	for i < len(formula) {
		idx := strings.Index(formula[i:], oldStr)
		if idx == -1 {
			result.WriteString(formula[i:])
			break
		}

		idx += i
		// Check if it's a standalone number (not part of a larger number)
		isStandalone := true
		if idx > 0 {
			prev := formula[idx-1]
			if (prev >= '0' && prev <= '9') || prev == '.' {
				isStandalone = false
			}
		}
		if idx+len(oldStr) < len(formula) {
			next := formula[idx+len(oldStr)]
			if (next >= '0' && next <= '9') || next == '.' {
				isStandalone = false
			}
		}

		if isStandalone {
			if count == n {
				result.WriteString(formula[i:idx])
				result.WriteString(newStr)
				result.WriteString(formula[idx+len(oldStr):])
				return result.String()
			}
			count++
		}

		result.WriteString(formula[i : idx+len(oldStr)])
		i = idx + len(oldStr)
	}

	return formula
}
