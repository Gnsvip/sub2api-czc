package service

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed pelican_worker/worker.py pelican_worker/pelican_eval/*.py pelican_worker/LICENSE
var pelicanWorkerFiles embed.FS

type PelicanVisualDimension struct {
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

type PelicanVisualAssessment struct {
	State       string                            `json:"state"`
	Score       *float64                          `json:"score,omitempty"`
	Threshold   int                               `json:"threshold"`
	Reason      string                            `json:"reason"`
	Rubric      string                            `json:"rubric,omitempty"`
	JudgeModel  string                            `json:"judge_model,omitempty"`
	SampleCount int                               `json:"sample_count,omitempty"`
	Dimensions  map[string]PelicanVisualDimension `json:"dimensions,omitempty"`
	FrameTimes  []float64                         `json:"frame_times,omitempty"`
	Checklist   map[string]float64                `json:"checklist,omitempty"`
}

var pelicanJudgeSlots = make(chan struct{}, 2)

func pelicanJudgePython() string { return strings.TrimSpace(os.Getenv("SUB2API_PELICAN_PYTHON")) }

func pelicanJudgeConfigured() bool {
	python := pelicanJudgePython()
	if python == "" {
		return false
	}
	if _, err := exec.LookPath(python); err != nil {
		return false
	}
	for _, name := range []string{"JUDGE_BASE_URL", "JUDGE_MODEL", "JUDGE_API_KEY"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return false
		}
	}
	return true
}

func pelicanPassScore(cfg IntelligentTestConfig) int {
	if cfg.VisualPassScore > 0 {
		return cfg.VisualPassScore
	}
	return 70
}

func unavailablePelicanAssessment(threshold int, reason string) *PelicanVisualAssessment {
	return &PelicanVisualAssessment{State: "unavailable", Threshold: threshold, Reason: reason}
}

type limitedPelicanOutput struct{ buffer bytes.Buffer }

func (b *limitedPelicanOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedPelicanOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 32<<10 {
		return 0, errors.New("visual judge output too large")
	}
	return b.buffer.Write(p)
}

func runAutoPelicanJudge(ctx context.Context, output, prompt string, threshold int) *PelicanVisualAssessment {
	fallback := unavailablePelicanAssessment(threshold, "视觉评分未完成，未判定画面质量")
	animal := intelligentJudgeAnimal(prompt)
	if animal == "" {
		return unavailablePelicanAssessment(threshold, "未识别测试题目中的动物，未判定画面质量")
	}
	if !pelicanJudgeConfigured() || len(output) > 256<<10 {
		return fallback
	}
	// Generation has its own deadline. The queue lease reserves 90 seconds;
	// judging (including waiting for a worker) must finish within that reserve.
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	select {
	case pelicanJudgeSlots <- struct{}{}:
		defer func() { <-pelicanJudgeSlots }()
	case <-ctx.Done():
		return fallback
	}
	dir, err := os.MkdirTemp("", "sub2api-pelican-")
	if err != nil {
		return fallback
	}
	defer os.RemoveAll(dir)
	err = fs.WalkDir(pelicanWorkerFiles, "pelican_worker", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "pelican_worker" {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(path, "pelican_worker/")))
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := pelicanWorkerFiles.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
	if err != nil {
		return fallback
	}
	payload, err := json.Marshal(map[string]any{"output": output, "threshold": threshold, "subject": animal})
	if err != nil {
		return fallback
	}
	cmd := exec.CommandContext(ctx, pelicanJudgePython(), "-B", filepath.Join(dir, "worker.py"))
	cmd.Dir = dir
	// Pass only the worker's required environment, never database or upstream
	// account credentials from the server process.
	for _, name := range []string{"PATH", "SYSTEMROOT", "HOME", "LANG", "LD_LIBRARY_PATH", "SSL_CERT_FILE", "SSL_CERT_DIR", "JUDGE_BASE_URL", "JUDGE_MODEL", "JUDGE_API_KEY", "JUDGE_PROTOCOL", "JUDGE_SAMPLES"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1")
	cmd.Stdin = bytes.NewReader(payload)
	var stdout limitedPelicanOutput
	cmd.Stdout = &stdout
	// stderr may contain provider responses; discard it rather than persist
	// credentials or raw errors in a public result.
	if err := cmd.Run(); err != nil {
		return fallback
	}
	return decodePelicanVisualAssessment(stdout.Bytes(), threshold)
}

func decodePelicanVisualAssessment(raw []byte, threshold int) *PelicanVisualAssessment {
	fallback := unavailablePelicanAssessment(threshold, "视觉评分未完成，未判定画面质量")
	var result PelicanVisualAssessment
	if json.Unmarshal(raw, &result) != nil {
		return fallback
	}
	result.Threshold = threshold
	if result.State == "unavailable" {
		result.Score, result.Dimensions, result.Checklist = nil, nil, nil
		return &result
	}
	if result.State != "passed" && result.State != "anomalous" || result.Score == nil || math.IsNaN(*result.Score) || math.IsInf(*result.Score, 0) || *result.Score < 0 || *result.Score > 100 {
		return fallback
	}
	if result.SampleCount < 1 || result.SampleCount > 3 || len(result.FrameTimes) != 6 || (result.Rubric != "pelican-judge-v1" && result.Rubric != "animal-judge-v1") {
		return fallback
	}
	for _, key := range []string{"prompt_fidelity", "object_quality", "interaction_spatial_correctness", "animation_quality", "visual_coherence", "aesthetic_quality"} {
		dimension, ok := result.Dimensions[key]
		if !ok || dimension.Score < 0 || dimension.Score > 5 {
			return fallback
		}
	}
	result.State = "anomalous"
	if *result.Score >= float64(threshold) {
		result.State = "passed"
	}
	return &result
}

func (s *IntelligentTestService) scorePelican(ctx context.Context, r *IntelligentTestRecord) {
	if r.TestType != "pelican" || r.ConfigSnapshot == nil || !r.ConfigSnapshot.VisualJudgeEnabled {
		return
	}
	if r.Evaluation == nil {
		r.Evaluation = newIntelligentAssessment("svg_structure")
	}
	judge := s.visualJudge
	if judge == nil {
		judge = runAutoPelicanJudge
	}
	result := judge(ctx, r.Result, r.ConfigSnapshot.Prompt, pelicanPassScore(*r.ConfigSnapshot))
	r.Evaluation["visual_judgement"] = result
	r.Score = result.Score
	// This observation never changes execution status or invokes account
	// repositories. Only the independent Astra probe may move groups.
}

func publicPelicanVisualAssessment(evaluation map[string]any) *PelicanVisualAssessment {
	raw, err := json.Marshal(evaluation["visual_judgement"])
	if err != nil || string(raw) == "null" {
		return nil
	}
	var result PelicanVisualAssessment
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return &result
}
