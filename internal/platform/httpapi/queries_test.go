package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mar-backend/internal/documents"
	"mar-backend/internal/platform/config"
	"mar-backend/internal/rag"
)

type querySearch func(context.Context, string, string, string, int) ([]documents.Result, error)

func (f querySearch) Search(ctx context.Context, tenant, question, resourceID string, limit int) ([]documents.Result, error) {
	return f(ctx, tenant, question, resourceID, limit)
}

type queryModelTransport func(http.ResponseWriter, *http.Request)

func (f queryModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	f(rec, req)
	return rec.Result(), nil
}

func TestQueryRanksEvidenceAndAnswersWithVerifiedSource(t *testing.T) {
	const resource = "1bdd98eb-fe02-5078-87d6-a32d52a2d44d"
	const question = "¿Cuánto se paga por día?"
	const expectedAnswer = "Se pagan 55 euros."
	const fragmentText = "La empresa paga 55 euros al día."
	var searches, turns, ranks int
	search := querySearch(func(_ context.Context, tenant, query, id string, limit int) ([]documents.Result, error) {
		searches++
		if tenant != "pruebas" || id != resource || limit != 16 {
			t.Errorf("alcance de la búsqueda %q / %q / %d", tenant, id, limit)
		}
		if searches == 1 && query != question || searches == 2 && query != "importe de las dietas" {
			t.Errorf("consulta inesperada %q", query)
		}
		return []documents.Result{
			{ResourceID: id, FileName: "dietas.pdf", FragmentID: "primero", Page: 3, Text: "Las dietas se registran."},
			{ResourceID: id, FileName: "dietas.pdf", FragmentID: "segundo", Page: 6, Text: fragmentText},
		}, nil
	})

	models := queryModelTransport(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("Authorization") != "Bearer token-prueba" {
			t.Error("se esperaba la clave del modelo")
		}
		switch req.URL.Path {
		case "/rerank":
			ranks++
			var body struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || len(body.Documents) != 2 || body.Documents[1] != fragmentText {
				t.Errorf("petición inválida al reranker: %+v (%v)", body, err)
			}
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.1},{"index":1,"relevance_score":0.9}]}`)
		case "/v1/chat/completions":
			turns++
			var body struct {
				Messages       []rag.Message  `json:"messages"`
				Tools          []rag.Tool     `json:"tools"`
				ResponseFormat map[string]any `json:"response_format"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("petición inválida al modelo: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			result := rag.Message{Role: "assistant"}
			switch turns {
			case 1:
				if len(body.Tools) != 2 || !strings.Contains(body.Messages[2].Content, `"source_id":1,"resource_id":"`+resource+`","file_name":"dietas.pdf","fragment_id":"segundo"`) {
					t.Errorf("orden o herramientas inesperados: %+v", body)
				}
				result.ToolCalls = []rag.ToolCall{
					{Type: "function", ID: "call1", Function: rag.FunctionCall{Name: "search_documents", Arguments: `{"query":"importe de las dietas"}`}},
					{Type: "function", ID: "call2", Function: rag.FunctionCall{Name: "get_document_fragment", Arguments: `{"source_id":1}`}},
				}
			case 2:
				last := body.Messages[len(body.Messages)-1]
				if len(body.Tools) != 0 || body.ResponseFormat["type"] != "json_schema" || last.ToolCallID != "call2" || !strings.Contains(last.Content, fragmentText) {
					t.Errorf("turno final inesperado: %+v", body)
				}
				result.Content = `{"quotes":[{"source_id":1,"quote":"` + fragmentText + `"}]}`
			case 3:
				if len(body.Tools) != 0 || len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, fragmentText) ||
					strings.Contains(body.Messages[1].Content, expectedAnswer) {
					t.Errorf("faltan datos en el turno de verificación: %+v", body)
				}
				result.Content = `{"reason":"El fragmento da el importe pedido.","supported":true}`
			case 4:
				if len(body.Tools) != 0 || len(body.Messages) != 3 || !strings.Contains(body.Messages[2].Content, fragmentText) ||
					strings.Contains(body.Messages[2].Content, "Las dietas se registran.") {
					t.Errorf("solo deben llegar las citas comprobadas a la redacción: %+v", body)
				}
				result.Content = `{"answer":"` + expectedAnswer + `","source_ids":[1,1]}`
			default:
				t.Errorf("turno fuera de presupuesto %d", turns)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": result}}})
		default:
			t.Errorf("ruta de inferencia inesperada %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	endpoint := rag.ModelEndpoint{URL: "http://modelos", Model: "pruebas", APIKey: "token-prueba",
		Client: &http.Client{Transport: models}, MaxResponseBytes: 4096}
	cfg := config.Config{Env: config.EnvTest, Tenancy: config.TenancyConfig{Header: "X-Tenant-ID", RequireHeader: true}}
	cfg.Server.RequestTimeout = time.Second
	router := NewRouter(RouterDeps{Config: cfg, Logger: discardLogger(), Queries: &rag.Service{
		Search: search, Ranker: rag.HTTPReranker{ModelEndpoint: endpoint}, Chat: rag.HTTPChat{ModelEndpoint: endpoint},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/queries", strings.NewReader(`{"query":"`+question+`","resource_id":"`+strings.ToUpper(resource)+`"}`))
	req.Header.Set("X-Tenant-ID", "pruebas")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("estado %d: %s", rec.Code, rec.Body)
	}
	var result rag.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "answered" || result.Answer != expectedAnswer || len(result.Sources) != 1 || result.Sources[0].Page != 6 ||
		result.Sources[0].FragmentID != "segundo" || searches != 2 || ranks != 2 || turns != 4 {
		t.Fatalf("respuesta o llamadas inesperadas: %+v (búsquedas %d, rerank %d, turnos %d)", result, searches, ranks, turns)
	}
}

func TestQueryAbstainsWhenRetrievedTextIsUnrelated(t *testing.T) {
	for _, tc := range []struct {
		name, question, passage string
		score                   float64
	}{
		{"otro tema", "¿Qué archivos hay que volver a pedir para rehacer el índice?", "Los sábados el almacén abre a las 10:00.", 0.000016109},
		{"término diferente", "en que informe deben constar las ostias", "Las dietas deben constar en el informe mensual.", 0.032348},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkQueryAbstains(t, tc.question, tc.passage, tc.score)
		})
	}
}

func checkQueryAbstains(t *testing.T, question, passage string, score float64) {
	t.Helper()
	models := queryModelTransport(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/rerank" {
			t.Errorf("no se debe llamar al modelo de respuesta con fragmentos irrelevantes: %s", req.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"index": 0, "relevance_score": score}},
		})
	})
	endpoint := rag.ModelEndpoint{URL: "http://modelos", Model: "pruebas",
		Client: &http.Client{Transport: models}, MaxResponseBytes: 4096}
	search := querySearch(func(context.Context, string, string, string, int) ([]documents.Result, error) {
		return []documents.Result{
			{FragmentID: "pasaje", Text: passage},
		}, nil
	})
	cfg := config.Config{Env: config.EnvTest, Tenancy: config.TenancyConfig{Header: "X-Tenant-ID", RequireHeader: true}}
	router := NewRouter(RouterDeps{Config: cfg, Logger: discardLogger(), Queries: &rag.Service{
		Search: search, Ranker: rag.HTTPReranker{ModelEndpoint: endpoint}, Chat: rag.HTTPChat{ModelEndpoint: endpoint},
	}})
	body, err := json.Marshal(map[string]string{"query": question})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/queries", strings.NewReader(string(body)))
	req.Header.Set("X-Tenant-ID", "pruebas")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result rag.Response
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	if rec.Code != http.StatusOK || err != nil || result.Status != "insufficient_evidence" || result.Sources == nil || len(result.Sources) != 0 {
		t.Fatalf("se esperaba falta de evidencia con una lista vacía de fuentes: HTTP %d, %s (%v)", rec.Code, rec.Body, err)
	}
}

func TestQueryRejectsCitationsThatOnlyAnswerRelatedQuestion(t *testing.T) {
	const question = "y los domingos a que hora abre?"
	const passage = "Los sábados el almacén abre a las 10:00."
	var verified bool
	models := queryModelTransport(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/rerank":
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.35089}]}`)
		case "/v1/chat/completions":
			var body struct {
				Messages []rag.Message `json:"messages"`
				Tools    []rag.Tool    `json:"tools"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("petición de inferencia inválida: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			content := `{"quotes":[{"source_id":1,"quote":"` + passage + `"}]}`
			if len(body.Tools) == 0 {
				if verified {
					t.Error("no debe haber redacción cuando la evidencia no responde a la pregunta")
				}
				verified = true
				if len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, question) ||
					!strings.Contains(body.Messages[1].Content, passage) {
					t.Errorf("la verificación necesita la pregunta original y el pasaje citado: %+v", body.Messages)
				}
				content = `{"reason":"La pregunta pide el domingo y el texto habla del sábado.","supported":false}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": rag.Message{Role: "assistant", Content: content}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	endpoint := rag.ModelEndpoint{URL: "http://modelos", Model: "pruebas",
		Client: &http.Client{Transport: models}, MaxResponseBytes: 4096}
	search := querySearch(func(context.Context, string, string, string, int) ([]documents.Result, error) {
		return []documents.Result{{FileName: "horarios.txt", FragmentID: "sabado", Text: passage}}, nil
	})
	cfg := config.Config{Env: config.EnvTest, Tenancy: config.TenancyConfig{Header: "X-Tenant-ID", RequireHeader: true}}
	router := NewRouter(RouterDeps{Config: cfg, Logger: discardLogger(), Queries: &rag.Service{
		Search: search, Ranker: rag.HTTPReranker{ModelEndpoint: endpoint}, Chat: rag.HTTPChat{ModelEndpoint: endpoint},
	}})
	req := httptest.NewRequest(http.MethodPost, "/v1/queries", strings.NewReader(`{"query":"`+question+`"}`))
	req.Header.Set("X-Tenant-ID", "pruebas")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result rag.Response
	err := json.Unmarshal(rec.Body.Bytes(), &result)
	if !verified || rec.Code != http.StatusOK || err != nil || result.Status != "insufficient_evidence" || result.Sources == nil || len(result.Sources) != 0 {
		t.Fatalf("se esperaba falta de evidencia tras la verificación: HTTP %d, %s (%v)", rec.Code, rec.Body, err)
	}
}
