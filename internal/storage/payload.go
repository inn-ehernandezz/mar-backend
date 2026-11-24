package storage

// Nombres de los campos de payload almacenados junto a cada vector. Se
// mantienen estables porque forman parte del contrato del índice: cambiar un
// nombre obliga a reindexar. El esquema está versionado mediante
// PayloadSchemaVersion.
const (
	FieldTenantID         = "tenant_id"
	FieldResourceID       = "resource_id"
	FieldFragmentID       = "fragment_id"
	FieldJobID            = "job_id"
	FieldContentType      = "content_type"
	FieldOriginalBlobPath = "original_blob_path"
	FieldDerivedBlobPath  = "derived_blob_path"
	FieldPageNumber       = "page_number"
	FieldSection          = "section"
	FieldTimestampStart   = "timestamp_start"
	FieldTimestampEnd     = "timestamp_end"
	FieldModelName        = "model_name"
	FieldModelVersion     = "model_version"
	FieldCreatedAt        = "created_at"
	FieldSchemaVersion    = "schema_version"
)

// PayloadSchemaVersion es la versión del esquema de payload. Todo punto
// indexado debe incluir este campo para poder migrar el esquema en el futuro.
const PayloadSchemaVersion = 1
