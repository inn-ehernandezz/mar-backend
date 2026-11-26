package rag

import (
	"context"
	"slices"
	"testing"
)

func TestAnswerRequiresSourcesWithVerifiedQuotes(t *testing.T) {
	allowed := map[int]Source{1: {FragmentID: "existente", Page: 6}}
	for _, tc := range []struct {
		name, content, status string
	}{
		{"sin evidencia", `{"answer":"","source_ids":[]}`, "insufficient_evidence"},
		{"fuente inventada", `{"answer":"Dato inventado","source_ids":[1,9]}`, "insufficient_evidence"},
		{"sin referencias", `{"answer":"Dato sin fuente","source_ids":[]}`, "insufficient_evidence"},
		{"fuente recuperada", `{"answer":"Respuesta","source_ids":[1]}`, "answered"},
		{"objeto JSON en bloque", "```json\n{\"answer\":\"Respuesta\",\"source_ids\":[1]}\n```", "answered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := answerFromEvidence(tc.content, allowed)
			if err != nil || got.Status != tc.status {
				t.Fatalf("respuesta %+v, error %v", got, err)
			}
			if tc.status == "insufficient_evidence" && (len(got.Sources) != 0 || got.Answer != insufficient().Answer) {
				t.Fatalf("se devolvió información sin fuente: %+v", got)
			}
		})
	}
}

func TestSelectQuotesRequiresExactTextFromTheNamedSource(t *testing.T) {
	session := querySession{sources: []Source{
		{Text: "El sábado abre a las 10:00. El domingo permanece cerrado."},
		{Text: "El plazo de devolución es de diez días."},
	}}
	for _, tc := range []struct {
		name, content string
		count         int
	}{
		{"cita literal", `{"quotes":[{"source_id":1,"quote":"El sábado abre a las 10:00."}]}`, 1},
		{"acción cambiada", `{"quotes":[{"source_id":1,"quote":"El sábado cierra a las 10:00."}]}`, 0},
		{"fuente equivocada", `{"quotes":[{"source_id":2,"quote":"El sábado abre a las 10:00."}]}`, 0},
		{"fuente desconocida", `{"quotes":[{"source_id":3,"quote":"El sábado abre a las 10:00."}]}`, 0},
		{"cita vacía", `{"quotes":[{"source_id":1,"quote":" "}]}`, 0},
		{"sin evidencia", `{"quotes":[]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := session.selectQuotes(tc.content)
			if err != nil || len(got) != tc.count {
				t.Fatalf("se recibieron %d citas, error %v", len(got), err)
			}
			if tc.count > 0 && got[0].Text != session.sources[0].Text {
				t.Fatalf("se perdió el contexto de la cita: %q", got[0].Text)
			}
		})
	}
}

type testRanker func(string, []string) ([]int, error)

func (r testRanker) Rank(_ context.Context, question string, texts []string) ([]int, error) {
	return r(question, texts)
}

func TestRankQuotedSentencesUsesTheOriginalQuestionAndKeepsProvenance(t *testing.T) {
	const question = "¿Qué hay que volver a pedir?"
	const opening = "Se conservan los vectores."
	const answer = "Habría que volver a pedir los archivos originales."
	service := Service{Ranker: testRanker(func(query string, texts []string) ([]int, error) {
		if query != question || !slices.Equal(texts, []string{opening, answer}) {
			t.Fatalf("entrada al reranker %q, %q", query, texts)
		}
		return []int{1}, nil
	})}

	got, err := service.rankQuotedSentences(t.Context(), question, []passageQuote{{ID: 6, Text: opening + " " + answer}})
	if err != nil || !slices.Equal(got, []passageQuote{{ID: 6, Text: answer}}) {
		t.Fatalf("citas %v, error %v", got, err)
	}
}
