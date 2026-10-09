package main

import (
	"github.com/ruoxizhnya/quant-trading/pkg/httpserver"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
)

// PipelineHandler handles pipeline-related HTTP requests
type PipelineHandler struct {
	pipeline *pipeline.Pipeline
	// runner is the BacktestRunner used by RunPipeline to execute the
	// backtest stage of the AI strategy generation pipeline. When nil,
	// the pipeline silently skips Stage 5 (backtest) — preserving the
	// pre-S7-P0-1 behaviour for callers that do not supply a runner.
	//
	// S7-P0-1 (ODR-043-1): previously RunPipeline hardcoded nil here,
	// which meant the AI pipeline was never end-to-end runnable. The
	// handler now accepts a runner via the WithBacktestRunner option
	// so main.go can inject the same copilotRunner already wired into
	// /api/copilot.
	runner pipeline.BacktestRunner
	// expSink 是实验日志的落点（P1-1b）。nil 表示这一路不记日志。
	expSink pipeline.ExperimentSink
}

// PipelineHandlerOption configures a PipelineHandler at construction
// time using the functional-options pattern.
type PipelineHandlerOption func(*PipelineHandler)

// WithBacktestRunner injects a BacktestRunner into the handler. Passing
// nil is allowed and equivalent to not calling the option — the pipeline
// will simply skip the backtest stage. This keeps the handler safe to
// construct without a runner (e.g. in tests or degraded deployments).
func WithBacktestRunner(r pipeline.BacktestRunner) PipelineHandlerOption {
	return func(h *PipelineHandler) {
		h.runner = r
	}
}

// WithExperimentSink 注入实验日志落点（P1-1b）。
//
// pipeline 有能力记日志还不够 —— 服务启动时必须真的把 sink 接上。少了这一步，
// 线上跑一百次实验库里依然一行都没有，而过拟合检测完全建立在「试了多少次」
// 这个数字上。不调用这个 option 就维持不记日志的旧行为。
func WithExperimentSink(s pipeline.ExperimentSink) PipelineHandlerOption {
	return func(h *PipelineHandler) {
		h.expSink = s
	}
}

// NewPipelineHandler creates a new pipeline handler. Default behaviour
// (no options) leaves the runner nil for backward compatibility —
// callers that need end-to-end pipeline execution should pass
// WithBacktestRunner.
func NewPipelineHandler(opts ...PipelineHandlerOption) *PipelineHandler {
	h := &PipelineHandler{}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	// pipeline 必须在 options 收集完之后再构造 —— sink 是构造时注入的，
	// 先构造再注入就永远接不上。
	var popts []pipeline.PipelineOption
	if h.expSink != nil {
		popts = append(popts, pipeline.WithExperimentSink(h.expSink))
	}
	h.pipeline = pipeline.NewPipeline(popts...)
	return h
}

// registerPipelineRoutes registers pipeline routes with the router.
// The runner parameter is forwarded to the handler via WithBacktestRunner
// so that the AI pipeline can execute the backtest stage end-to-end;
// pass nil to keep the legacy "skip backtest" behaviour.
func registerPipelineRoutes(router *gin.Engine, runner pipeline.BacktestRunner, sink pipeline.ExperimentSink) {
	opts := []PipelineHandlerOption{WithBacktestRunner(runner)}
	if sink != nil {
		opts = append(opts, WithExperimentSink(sink))
	}
	handler := NewPipelineHandler(opts...)
	api := router.Group("/api")
	handler.RegisterPipelineRoutes(api)
}

// RunPipelineRequest represents a request to run the pipeline
// @Description Request body for running the AI strategy generation pipeline
// @Description 运行AI策略生成流水线的请求体
type RunPipelineRequest struct {
	Description string `json:"description" binding:"required" example:"20日动量策略，在沪深300中选出最强10只股票"`
}

// PipelineJobResponse represents a pipeline job result
// @Description Pipeline job execution result
// @Description 流水线作业执行结果
type PipelineJobResponse struct {
	ID             string      `json:"id"`
	Status         string      `json:"status"`
	Intent         interface{} `json:"intent,omitempty"`
	YAMLConfig     string      `json:"yaml_config,omitempty"`
	GeneratedCode  string      `json:"generated_code,omitempty"`
	BuildError     string      `json:"build_error,omitempty"`
	BacktestResult interface{} `json:"backtest_result,omitempty"`
	BacktestError  string      `json:"backtest_error,omitempty"`
	StartedAt      string      `json:"started_at"`
	CompletedAt    *string     `json:"completed_at,omitempty"`
	DurationMs     int64       `json:"duration_ms"`
	Logs           []string    `json:"logs,omitempty"`
}

// RegisterPipelineRoutes registers pipeline routes
func (h *PipelineHandler) RegisterPipelineRoutes(router *gin.RouterGroup) {
	pipeline := router.Group("/pipeline")
	{
		pipeline.POST("/run", h.RunPipeline)
		pipeline.GET("/jobs", h.GetPipelineJobs)
		pipeline.GET("/jobs/:id", h.GetPipelineJob)
	}
}

// RunPipeline runs the full AI strategy generation pipeline
// @Summary      Run AI Strategy Pipeline
// @Description  Execute the full pipeline: intent parsing -> YAML generation -> code generation -> compilation -> backtest
// @Description  执行完整的AI策略生成流水线：意图解析->YAML生成->代码生成->编译验证->回测
// @Tags         AI Pipeline
// @Accept       json
// @Produce      json
// @Param        request  body      RunPipelineRequest  true  "Pipeline request"
// @Success      200      {object}  PipelineJobResponse  "Pipeline execution result"
// @Failure      400      {object}  map[string]string    "Invalid request"
// @Failure      500      {object}  map[string]string    "Internal server error"
// @Router       /pipeline/run [post]
func (h *PipelineHandler) RunPipeline(c *gin.Context) {
	var req RunPipelineRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}

	// S7-P0-1 (ODR-043-1): pass the injected runner instead of nil so the
	// pipeline can actually execute the backtest stage. When no runner was
	// injected (legacy callers), h.runner is nil and Execute still skips
	// Stage 5 gracefully — preserving backward compatibility.
	result, err := h.pipeline.Execute(c.Request.Context(), req.Description, h.runner)
	if err != nil {
		// Return the result even if there was an error, so the client can see partial results
		c.JSON(http.StatusOK, pipelineResultToResponse(result))
		return
	}

	c.JSON(http.StatusOK, pipelineResultToResponse(result))
}

// GetPipelineJobs returns all pipeline jobs
// @Summary      List Pipeline Jobs
// @Description  Get a list of all pipeline jobs and their status
// @Description  获取所有流水线作业及其状态
// @Tags         AI Pipeline
// @Produce      json
// @Success      200  {array}   PipelineJobResponse  "List of pipeline jobs"
// @Router       /pipeline/jobs [get]
func (h *PipelineHandler) GetPipelineJobs(c *gin.Context) {
	// For now, return an empty list
	// In a production system, this would iterate through all jobs in the pipeline
	c.JSON(http.StatusOK, []PipelineJobResponse{})
}

// GetPipelineJob returns a specific pipeline job by ID
// @Summary      Get Pipeline Job
// @Description  Get the status and result of a specific pipeline job
// @Description  获取特定流水线作业的状态和结果
// @Tags         AI Pipeline
// @Produce      json
// @Param        id   path      string               true  "Job ID"
// @Success      200  {object}  PipelineJobResponse  "Pipeline job result"
// @Failure      404  {object}  map[string]string    "Job not found"
// @Router       /pipeline/jobs/{id} [get]
func (h *PipelineHandler) GetPipelineJob(c *gin.Context) {
	jobID := c.Param("id")
	result := h.pipeline.GetJob(jobID)
	if result == nil {
		httpserver.Fail(c, http.StatusNotFound, "job not found")
		return
	}

	c.JSON(http.StatusOK, pipelineResultToResponse(result))
}

// pipelineResultToResponse converts a pipeline.Result to a PipelineJobResponse
func pipelineResultToResponse(result *pipeline.Result) PipelineJobResponse {
	if result == nil {
		return PipelineJobResponse{}
	}

	resp := PipelineJobResponse{
		ID:            result.ID,
		Status:        string(result.Status),
		YAMLConfig:    result.YAMLConfig,
		GeneratedCode: result.GeneratedCode,
		BuildError:    result.BuildError,
		BacktestError: result.BacktestError,
		StartedAt:     result.StartedAt.Format("2006-01-02T15:04:05Z"),
		DurationMs:    result.DurationMs,
		Logs:          result.Logs,
	}

	if result.Intent != nil {
		resp.Intent = result.Intent
	}

	if result.BacktestResult != nil {
		resp.BacktestResult = result.BacktestResult
	}

	if result.CompletedAt != nil {
		completedAt := result.CompletedAt.Format("2006-01-02T15:04:05Z")
		resp.CompletedAt = &completedAt
	}

	return resp
}
