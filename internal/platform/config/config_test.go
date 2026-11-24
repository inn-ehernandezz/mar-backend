package config

import (
	"strings"
	"testing"
)

func TestDefaultsValidateInDevelopment(t *testing.T) {
	cfg := defaults()
	cfg.Env = EnvDevelopment

	if err := cfg.Validate(); err != nil {
		t.Fatalf("la configuración por defecto debe ser válida en desarrollo: %v", err)
	}
}

func TestValidateRejectsMockInferenceInProduction(t *testing.T) {
	cfg := defaults()
	cfg.Env = EnvProduction
	cfg.Qdrant.URL = "https://qdrant.example.azure.net"
	cfg.Blob.Endpoint = "https://acct.blob.core.windows.net"
	cfg.Table.Endpoint = "https://acct.table.core.windows.net"
	cfg.Inference.MockEnabled = true

	err := cfg.Validate()
	if err == nil {
		t.Fatal("se esperaba un error por habilitar inferencia simulada en producción")
	}
	if !strings.Contains(err.Error(), "MAR_INFERENCE_MOCK_ENABLED") {
		t.Errorf("el error debe mencionar MAR_INFERENCE_MOCK_ENABLED: %v", err)
	}
}

func TestValidateRejectsLocalEndpointsInProduction(t *testing.T) {
	cfg := defaults()
	cfg.Env = EnvProduction

	if err := cfg.Validate(); err == nil {
		t.Fatal("se esperaba un error por usar endpoints locales en producción")
	}
}

func TestValidateAcceptsRemoteEndpointsInProduction(t *testing.T) {
	cfg := defaults()
	cfg.Env = EnvProduction
	cfg.Qdrant.URL = "https://qdrant.example.azure.net"
	cfg.Blob.Endpoint = "https://acct.blob.core.windows.net"
	cfg.Table.Endpoint = "https://acct.table.core.windows.net"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("la configuración de producción debe ser válida: %v", err)
	}
}

func TestValidateRejectsDevTenantInProduction(t *testing.T) {
	cfg := defaults()
	cfg.Env = EnvProduction
	cfg.Qdrant.URL = "https://qdrant.example.azure.net"
	cfg.Blob.Endpoint = "https://acct.blob.core.windows.net"
	cfg.Table.Endpoint = "https://acct.table.core.windows.net"
	cfg.Tenancy.DevTenant = "dev"

	if err := cfg.Validate(); err == nil {
		t.Fatal("se esperaba un error por definir un tenant de desarrollo en producción")
	}
}

func TestSafeSummaryOmitsSecrets(t *testing.T) {
	cfg := defaults()
	cfg.Blob.AccountKey = "secreto-de-prueba"
	cfg.Qdrant.APIKey = "clave-qdrant"

	summary := cfg.SafeSummary()
	for key, value := range summary {
		if text, ok := value.(string); ok && strings.Contains(text, "secreto-de-prueba") {
			t.Errorf("el resumen no debe exponer secretos (clave %q)", key)
		}
	}
}
