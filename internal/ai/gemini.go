package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/kadi/backend/internal/db/queries"
	"google.golang.org/api/option"
)

// MatchAnalysis is the structured response returned to the frontend
// for the DeepDiveModal and AIAnalysisExplainer components.
type MatchAnalysis struct {
	Verdict       string   `json:"verdict"`        // e.g. "Home Win"
	Confidence    int      `json:"confidence"`     // 0-100
	Summary       string   `json:"summary"`        // 2-3 sentence overview
	KeyFactors    []string `json:"key_factors"`    // bullet points driving prediction
	Risks         []string `json:"risks"`          // risks to the prediction
	BettingAngle  string   `json:"betting_angle"`  // suggested market / angle
	FormAnalysis  string   `json:"form_analysis"`  // narrative on recent form
	H2HAnalysis   string   `json:"h2h_analysis"`   // historical head-to-head narrative
	RecommendedOddsRange string `json:"recommended_odds_range"` // e.g. "1.70 - 1.95"
}

// GeminiClient wraps the Google Generative AI SDK for structured sports analysis.
type GeminiClient struct {
	client *genai.Client
	model  string
}

// New creates a new GeminiClient using the provided API key and model name.
func New(ctx context.Context, apiKey, model string) (*GeminiClient, error) {
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("gemini: failed to create client: %w", err)
	}
	return &GeminiClient{client: client, model: model}, nil
}

// Close releases the underlying gRPC connection.
func (g *GeminiClient) Close() error {
	return g.client.Close()
}

// AnalyzeMatch performs a full deep-dive analysis on a fixture and returns
// a structured MatchAnalysis. It uses the specified model (e.g. flash vs pro).
func (g *GeminiClient) AnalyzeMatch(ctx context.Context, f *queries.Fixture, modelName string) (*MatchAnalysis, error) {
	prompt := buildDeepDivePrompt(f)
	
	// If no model provided, fallback to the default configured model
	if modelName == "" {
		modelName = g.model
	}

	raw, err := g.generate(ctx, prompt, modelName)
	if err != nil {
		return nil, err
	}

	// Extract JSON from the response (Gemini may wrap it in markdown code fences)
	jsonStr := extractJSON(raw)

	var analysis MatchAnalysis
	if err := json.Unmarshal([]byte(jsonStr), &analysis); err != nil {
		// Fallback: return a text-only summary if JSON parsing fails
		return &MatchAnalysis{
			Summary:    raw,
			Confidence: f.ProbabilityHome > f.ProbabilityAway ? int(f.ProbabilityHome) : int(f.ProbabilityAway),
		}, nil
	}
	return &analysis, nil
}

// ExplainPrediction returns a concise one-paragraph explanation for the
// AIAnalysisExplainer.tsx component (lighter, faster call).
func (g *GeminiClient) ExplainPrediction(ctx context.Context, f *queries.Fixture) (string, error) {
	prompt := buildExplainPrompt(f)
	return g.generate(ctx, prompt, g.model)
}

// ─── internals ──────────────────────────────────────────────────────────────

func (g *GeminiClient) generate(ctx context.Context, prompt, modelName string) (string, error) {
	model := g.client.GenerativeModel(modelName)

	// Configure the model for consistent, structured output
	model.SetTemperature(0.4)
	model.SetTopP(0.9)
	model.SetMaxOutputTokens(2048)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", fmt.Errorf("gemini: generate content: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini: empty response")
	}

	var sb strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			sb.WriteString(string(text))
		}
	}
	return sb.String(), nil
}

// buildDeepDivePrompt constructs a rich context prompt for full match analysis.
func buildDeepDivePrompt(f *queries.Fixture) string {
	homeForm := formatForm(f.HomeForm)
	awayForm := formatForm(f.AwayForm)

	return fmt.Sprintf(`You are Kadi AI, an elite sports analytics engine. Analyze the following match and return ONLY valid JSON — no markdown, no explanation outside the JSON.

MATCH CONTEXT:
- Sport: %s
- Home Team: %s (Form last 5 matches: %s)
- Away Team: %s (Form last 5 matches: %s)
- Head-to-Head: Home wins %d | Away wins %d | Draws %d
- Current Odds: Home %.2f | Draw %.2f | Away %.2f
- Model Probabilities: Home %.1f%% | Draw %.1f%% | Away %.1f%%
- Match Status: %s

Return this exact JSON structure:
{
  "verdict": "<Home Win | Away Win | Draw>",
  "confidence": <integer 0-100>,
  "summary": "<2-3 sentence match overview>",
  "key_factors": ["<factor 1>", "<factor 2>", "<factor 3>"],
  "risks": ["<risk 1>", "<risk 2>"],
  "betting_angle": "<recommended market and reasoning>",
  "form_analysis": "<narrative on recent form trends>",
  "h2h_analysis": "<historical head-to-head narrative>",
  "recommended_odds_range": "<e.g. 1.70 - 1.95>"
}`,
		f.Sport,
		f.HomeTeamName, homeForm,
		f.AwayTeamName, awayForm,
		f.H2HHomeWins, f.H2HAwayWins, f.H2HDraws,
		f.OddsHome, f.OddsDraw, f.OddsAway,
		f.ProbabilityHome, f.ProbabilityDraw, f.ProbabilityAway,
		f.Status,
	)
}

// buildExplainPrompt builds a lighter prompt for the explanation snippet.
func buildExplainPrompt(f *queries.Fixture) string {
	return fmt.Sprintf(`You are Kadi AI. In exactly 2-3 sentences, explain in simple terms why %s vs %s is predicted to result in a %s outcome (confidence: %.0f%%). Mention form and head-to-head briefly. Be direct and confident.`,
		f.HomeTeamName, f.AwayTeamName,
		predictedVerdict(f),
		max3(f.ProbabilityHome, f.ProbabilityDraw, f.ProbabilityAway),
	)
}

func formatForm(form []int) string {
	if len(form) == 0 {
		return "N/A"
	}
	parts := make([]string, len(form))
	for i, v := range form {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func extractJSON(s string) string {
	// Strip markdown code fences if present
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func predictedVerdict(f *queries.Fixture) string {
	if f.ProbabilityHome >= f.ProbabilityAway && f.ProbabilityHome >= f.ProbabilityDraw {
		return "Home Win"
	}
	if f.ProbabilityAway >= f.ProbabilityHome && f.ProbabilityAway >= f.ProbabilityDraw {
		return "Away Win"
	}
	return "Draw"
}

func max3(a, b, c float64) float64 {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}
