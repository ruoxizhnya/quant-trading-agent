package validator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// ValidationResult holds the result of code validation.
type ValidationResult struct {
	Valid       bool     `json:"valid"`
	Errors      []string `json:"errors,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	SyntaxValid bool     `json:"syntax_valid"`
	Compiles    bool     `json:"compiles"`
}

// CodeValidator validates generated strategy code.
type CodeValidator struct {
	tempDir string
	// workDir 是 `go build` 的工作目录。它必须是一个 module 根（含
	// go.mod），否则生成的策略代码里 `github.com/ruoxizhnya/...` 这类
	// import 解析不了，合法代码也会被判成编译失败。
	//
	// 构造时向上探测；探测不到就留空，此时 ValidateCompilation 会按
	// 「无法验证」处理 —— 宁可说不知道，也不能像以前那样谎报通过。
	workDir string
}

// NewCodeValidator creates a new code validator.
func NewCodeValidator() *CodeValidator {
	return &CodeValidator{
		tempDir: os.TempDir(),
		workDir: findModuleRoot(),
	}
}

// NewCodeValidatorWithWorkDir creates a validator that compiles in dir.
// 容器里二进制所在目录未必有 go.mod，那时要么把源码挂进去，要么接受
// 「无法验证」的结果。
func NewCodeValidatorWithWorkDir(dir string) *CodeValidator {
	v := NewCodeValidator()
	if dir != "" {
		v.workDir = dir
	}
	return v
}

// findModuleRoot walks upward from the working directory looking for
// go.mod. Returns "" when not found — callers must treat that as
// "cannot verify", never as "compiles".
func findModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ValidateSyntax checks if the code has valid Go syntax.
func (v *CodeValidator) ValidateSyntax(code string) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Errors:   []string{},
		Warnings: []string{},
	}

	// Basic syntax checks
	if strings.TrimSpace(code) == "" {
		result.Valid = false
		result.SyntaxValid = false
		result.Errors = append(result.Errors, "Empty code")
		return result
	}

	// Check for required imports
	if !strings.Contains(code, "package strategy") {
		result.Warnings = append(result.Warnings, "Missing 'package strategy' declaration")
	}

	// Check for Strategy interface implementation
	if !strings.Contains(code, "Strategy") {
		result.Warnings = append(result.Warnings, "May not implement Strategy interface")
	}

	// Check for common issues
	if strings.Count(code, "{") != strings.Count(code, "}") {
		result.Valid = false
		result.SyntaxValid = false
		result.Errors = append(result.Errors, "Mismatched braces")
	}

	if strings.Count(code, "(") != strings.Count(code, ")") {
		result.Valid = false
		result.SyntaxValid = false
		result.Errors = append(result.Errors, "Mismatched parentheses")
	}

	result.SyntaxValid = len(result.Errors) == 0
	return result
}

// ValidateCompilation attempts to compile the code.
func (v *CodeValidator) ValidateCompilation(code string) *ValidationResult {
	result := v.ValidateSyntax(code)
	if !result.SyntaxValid {
		return result
	}

	// Create temporary file
	tempFile := filepath.Join(v.tempDir, fmt.Sprintf("strategy_%d.go", time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, []byte(code), 0644); err != nil {
		result.Valid = false
		result.Compiles = false
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to write temp file: %v", err))
		return result
	}
	defer os.Remove(tempFile)

	// Try to compile with gofmt first
	cmd := exec.Command("gofmt", "-e", tempFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		result.Valid = false
		result.Compiles = false
		result.Errors = append(result.Errors, fmt.Sprintf("gofmt error: %s", string(output)))
		return result
	}

	// P0-6: 真的编译一遍。
	//
	// 此前这里是 `go tool compile -V=full` —— 那是**打印编译器版本号**
	// 的开关，压根不读后面的文件。于是只要 gofmt 过得去，Compiles 就
	// 恒为 true：调用方以为代码编译过了，实际上只验证了语法。
	if v.workDir == "" {
		result.Valid = false
		result.Compiles = false
		result.Errors = append(result.Errors,
			"cannot verify compilation: no go.mod found above the working directory "+
				"(use NewCodeValidatorWithWorkDir to point at the module root)")
		return result
	}

	cmd = exec.Command("go", "build", tempFile)
	cmd.Dir = v.workDir
	output, err = cmd.CombinedOutput()
	if err != nil {
		result.Valid = false
		result.Compiles = false
		result.Errors = append(result.Errors,
			fmt.Sprintf("compilation failed: %s", strings.TrimSpace(string(output))))
		return result
	}

	result.Compiles = true
	return result
}

// ValidateTemplate validates a strategy template by rendering and checking it.
func (v *CodeValidator) ValidateTemplate(templateStr string, data interface{}) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Errors:   []string{},
		Warnings: []string{},
	}

	// Parse template
	tmpl, err := template.New("strategy").Parse(templateStr)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Template parse error: %v", err))
		return result
	}

	// Render template
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Template execute error: %v", err))
		return result
	}

	// Validate rendered code
	rendered := buf.String()
	return v.ValidateSyntax(rendered)
}

// ValidateFull runs full validation: syntax + compilation.
func (v *CodeValidator) ValidateFull(code string) *ValidationResult {
	result := v.ValidateCompilation(code)
	result.Valid = result.SyntaxValid && result.Compiles
	return result
}

// QuickValidate runs a fast syntax-only validation.
func (v *CodeValidator) QuickValidate(code string) *ValidationResult {
	return v.ValidateSyntax(code)
}
