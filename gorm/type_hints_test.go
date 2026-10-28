package gorm_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	rds "github.com/krotscheck/go-rds-driver"
	rdsgorm "github.com/krotscheck/go-rds-driver/gorm"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type record struct {
	ID          int            `gorm:"primaryKey"`
	Payload     map[string]any `gorm:"type:jsonb;serializer:json"`
	Description string         `gorm:"type:text"`
}

type typedRecord struct {
	ID          int       `gorm:"primaryKey"`
	Payload     string    `gorm:"type:jsonb"`
	Date        time.Time `gorm:"type:date"`
	Amount      string    `gorm:"type:decimal(30,6)"`
	TimeOfDay   time.Time `gorm:"type:time"`
	OccurredAt  time.Time `gorm:"type:timestamp"`
	ExternalID  string    `gorm:"type:uuid"`
	Description string    `gorm:"type:text"`
}

type rawJSONRecord struct {
	ID    int             `gorm:"primaryKey"`
	Rules json.RawMessage `gorm:"type:jsonb;not null"`
}

type rawJSONPolicyRecord struct {
	ID          string          `gorm:"primaryKey"`
	Name        string          `gorm:"size:255;not null"`
	Description string          `gorm:"size:1024"`
	Rules       json.RawMessage `gorm:"type:jsonb;not null"`
	HTTPRules   json.RawMessage `gorm:"column:http_rules;type:jsonb"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func TestCreateAddsJSONHintOnlyToJSONSerializedFields(t *testing.T) {
	pool := &captureConnPool{}
	db := newTestDB(t, pool)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := record{
		ID:          1,
		Payload:     map[string]any{"mode": "strict"},
		Description: `{"mode":"strict"}`,
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(pool.execArgs) != 1 {
		t.Fatalf("expected one ExecContext call, got %d", len(pool.execArgs))
	}
	args := pool.execArgs[0]
	if len(args) != 3 {
		t.Fatalf("expected 3 bound values, got %d", len(args))
	}
	assertJSONAndTextArgs(t, args, `{"mode":"strict"}`, model.Description)
}

func TestUpdateAddsJSONHintToSerializedField(t *testing.T) {
	pool := &captureConnPool{}
	db := newTestDB(t, pool)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := record{
		ID:          1,
		Payload:     map[string]any{"mode": "strict"},
		Description: `{"mode":"strict"}`,
	}
	if err := db.Model(&model).Select("Payload", "Description").Updates(&model).Error; err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(pool.execArgs) != 1 {
		t.Fatalf("expected one ExecContext call, got %d", len(pool.execArgs))
	}
	args := pool.execArgs[0]
	if len(args) != 3 {
		t.Fatalf("expected 3 bound values, got %d", len(args))
	}
	assertJSONAndTextArgs(t, args, `{"mode":"strict"}`, model.Description)
}

func TestTypeHintPluginSupportsJSONRawMessage(t *testing.T) {
	pool := &captureConnPool{}
	db := newTestDB(t, pool)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := rawJSONRecord{
		ID:    1,
		Rules: json.RawMessage(`{"namespace":"pki","actions":["read"]}`),
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := db.Model(&model).Where("id = ?", model.ID).Select("Rules").Updates(&model).Error; err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(pool.execArgs) != 2 {
		t.Fatalf("expected create and update calls, got %d", len(pool.execArgs))
	}

	for _, args := range pool.execArgs {
		found := false
		for _, arg := range args {
			hinted, ok := arg.(rds.JSON)
			if !ok {
				continue
			}
			value, err := hinted.Value()
			if err != nil {
				t.Fatalf("read JSON parameter: %v", err)
			}
			if value != string(model.Rules) {
				t.Fatalf("JSON parameter = %q, want %q", value, model.Rules)
			}
			found = true
		}
		if !found {
			t.Fatal("expected JSON RawMessage parameter to carry the RDS JSON type hint")
		}
	}
}

func TestTypeHintPluginDoesNotShiftHintsForNullJSONFields(t *testing.T) {
	pool := &captureConnPool{}
	db := newTestDB(t, pool)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := rawJSONPolicyRecord{
		ID:        "policy-1",
		Name:      "Policy",
		Rules:     json.RawMessage(`[{"namespace":"pki","actions":["read"]}]`),
		CreatedAt: time.Date(2026, 10, 28, 10, 3, 8, 570735911, time.FixedZone("UTC+1", 3600)),
		UpdatedAt: time.Date(2026, 10, 28, 10, 3, 8, 570000000, time.FixedZone("UTC+1", 3600)),
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(pool.execArgs) != 1 {
		t.Fatalf("expected one ExecContext call, got %d", len(pool.execArgs))
	}

	jsonHintCount := 0
	timestampCount := 0
	for _, arg := range pool.execArgs[0] {
		if hinted, ok := arg.(rds.JSON); ok {
			jsonHintCount++
			value, err := hinted.Value()
			if err != nil {
				t.Fatalf("read JSON parameter: %v", err)
			}
			if value != string(model.Rules) {
				t.Fatalf("JSON parameter = %q, want rules JSON %q", value, model.Rules)
			}
		}
		if _, ok := arg.(time.Time); ok {
			timestampCount++
		}
	}
	if jsonHintCount != 1 {
		t.Fatalf("expected only the rules value to have a JSON hint, got %d JSON hints", jsonHintCount)
	}
	if timestampCount != 2 {
		t.Fatalf("expected both timestamps to remain time.Time values, got %d", timestampCount)
	}
}

func TestTypeHintPluginMapsDeclaredTypesForCreateAndUpdate(t *testing.T) {
	pool := &captureConnPool{}
	db := newTestDB(t, pool)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := newTypedRecord()
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := db.Model(&model).
		Select("Payload", "Date", "Amount", "TimeOfDay", "OccurredAt", "ExternalID", "Description").
		Updates(&model).Error; err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(pool.execArgs) != 2 {
		t.Fatalf("expected create and update calls, got %d", len(pool.execArgs))
	}

	expected := expectedTypeHintValues()
	for index, args := range pool.execArgs {
		assertTypeHintArgs(t, args, expected, model.Description)
		if index == 0 && len(args) != 8 {
			t.Errorf("create: expected 8 bound values, got %d", len(args))
		}
	}
}

func TestTypeHintPluginAddsUUIDHintToQueryPredicates(t *testing.T) {
	client := &captureRDSClient{}
	conf := rds.NewConfig("resource", "secret", "database", "region")
	sqlDB := sql.OpenDB(dataAPIConnector{client: client, config: conf})
	defer sqlDB.Close()

	db := newTestDB(t, sqlDB)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := newTypedRecord()
	var records []typedRecord
	if err := db.Where(clause.Eq{
		Column: clause.Column{Name: "ExternalID"},
		Value:  model.ExternalID,
	}).Where(clause.Eq{
		Column: clause.Column{Name: "Description"},
		Value:  model.Description,
	}).Find(&records).Error; err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(client.statements) != 1 {
		t.Fatalf("expected one Data API statement, got %d", len(client.statements))
	}

	parameters := client.statements[0].Parameters
	if len(parameters) != 2 {
		t.Fatalf("expected 2 bound values, got %d", len(parameters))
	}
	if parameters[0].TypeHint != types.TypeHintUuid {
		t.Errorf("expected UUID type hint, got %q", parameters[0].TypeHint)
	}
	if parameters[1].TypeHint != "" {
		t.Errorf("expected ordinary text to remain unhinted, got %q", parameters[1].TypeHint)
	}
}

func TestTypeHintPluginAddsUUIDHintToUpdateAndDeletePredicates(t *testing.T) {
	client := &captureRDSClient{}
	conf := rds.NewConfig("resource", "secret", "database", "region")
	sqlDB := sql.OpenDB(dataAPIConnector{client: client, config: conf})
	defer sqlDB.Close()

	db := newTestDB(t, sqlDB)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := newTypedRecord()
	if err := db.Model(&model).Where(clause.Eq{
		Column: clause.Column{Name: "ExternalID"},
		Value:  model.ExternalID,
	}).Update("Description", model.Description).Error; err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if err := db.Where(clause.Eq{
		Column: clause.Column{Name: "ExternalID"},
		Value:  model.ExternalID,
	}).Delete(&typedRecord{}).Error; err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(client.statements) != 2 {
		t.Fatalf("expected update and delete statements, got %d", len(client.statements))
	}

	for _, statement := range client.statements {
		foundUUIDHint := false
		for _, parameter := range statement.Parameters {
			if parameter.TypeHint == types.TypeHintUuid {
				foundUUIDHint = true
			}
		}
		if !foundUUIDHint {
			t.Errorf("expected UUID hint in statement parameters: %#v", statement.Parameters)
		}
	}
}

func TestTypeHintPluginPreservesTransactionIDThroughCreateAndUpdate(t *testing.T) {
	transactionID := "test-transaction-id"
	client := &captureRDSClient{transactionID: &transactionID}
	conf := rds.NewConfig("resource", "secret", "database", "region")
	sqlDB := sql.OpenDB(dataAPIConnector{client: client, config: conf})
	defer sqlDB.Close()

	db, err := gorm.Open(testDialector{pool: sqlDB}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("could not open test GORM DB: %v", err)
	}
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := newTypedRecord()
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := db.Model(&model).Where(clause.Eq{
		Column: clause.Column{Name: "ExternalID"},
		Value:  model.ExternalID,
	}).Update("Description", model.Description).Error; err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.begins) != 2 {
		t.Fatalf("expected two Data API begins, got %d", len(client.begins))
	}
	if len(client.statements) != 2 {
		t.Fatalf("expected two Data API statements, got %d", len(client.statements))
	}
	for _, statement := range client.statements {
		if got := statement.TransactionId; got == nil || *got != transactionID {
			t.Fatalf("statement transaction ID = %v, want %q", got, transactionID)
		}
	}
	if len(client.commits) != 2 {
		t.Fatalf("expected two Data API commits, got %d", len(client.commits))
	}
	for _, commit := range client.commits {
		if got := commit.TransactionId; got == nil || *got != transactionID {
			t.Fatalf("commit transaction ID = %v, want %q", got, transactionID)
		}
	}
}

func TestTypeHintPluginDoesNotAffectOtherGORMConnections(t *testing.T) {
	pool := &captureConnPool{}
	db := newTestDB(t, pool)
	model := record{
		ID:          1,
		Payload:     map[string]any{"mode": "strict"},
		Description: `{"mode":"strict"}`,
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(pool.execArgs) != 1 {
		t.Fatalf("expected one ExecContext call, got %d", len(pool.execArgs))
	}

	foundSerializedJSON := false
	for _, arg := range pool.execArgs[0] {
		if _, ok := arg.(rds.TypeHinter); ok {
			t.Fatalf("unconfigured GORM DB unexpectedly received a type hint: %T", arg)
		}
		if valuer, ok := arg.(driver.Valuer); ok {
			value, err := valuer.Value()
			if err != nil {
				t.Fatalf("could not read GORM JSON value: %v", err)
			}
			if value == `{"mode":"strict"}` {
				foundSerializedJSON = true
			}
		}
	}
	if !foundSerializedJSON {
		t.Fatal("expected ordinary GORM JSON serialization without a type hint")
	}
}

func TestGORMJSONHintReachesRDSDataAPIParameter(t *testing.T) {
	client := &captureRDSClient{}
	conf := rds.NewConfig("resource", "secret", "database", "region")
	sqlDB := sql.OpenDB(dataAPIConnector{client: client, config: conf})
	defer sqlDB.Close()

	db := newTestDB(t, sqlDB)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := record{
		ID:          1,
		Payload:     map[string]any{"mode": "strict"},
		Description: `{"mode":"strict"}`,
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.statements) != 1 {
		t.Fatalf("expected one Data API statement, got %d", len(client.statements))
	}

	jsonHintCount := 0
	plainJSONTextFound := false
	for _, parameter := range client.statements[0].Parameters {
		if parameter.TypeHint == types.TypeHintJson {
			jsonHintCount++
			continue
		}
		if parameter.TypeHint == "" {
			if value, ok := parameter.Value.(*types.FieldMemberStringValue); ok && value.Value == model.Description {
				plainJSONTextFound = true
			}
		}
	}
	if jsonHintCount != 1 {
		t.Errorf("expected one JSON type hint, got %d", jsonHintCount)
	}
	if !plainJSONTextFound {
		t.Error("expected JSON-looking text to reach the Data API without a type hint")
	}
}

func TestGORMTypeHintsReachRDSDataAPIParameters(t *testing.T) {
	client := &captureRDSClient{}
	conf := rds.NewConfig("resource", "secret", "database", "region")
	sqlDB := sql.OpenDB(dataAPIConnector{client: client, config: conf})
	defer sqlDB.Close()

	db := newTestDB(t, sqlDB)
	if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
		t.Fatal(err)
	}

	model := newTypedRecord()
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.statements) != 1 {
		t.Fatalf("expected one Data API statement, got %d", len(client.statements))
	}

	expected := expectedTypeHintValues()
	found := make(map[types.TypeHint]bool, len(expected))
	plainTextFound := false
	for _, parameter := range client.statements[0].Parameters {
		if parameter.TypeHint == "" {
			if value, ok := parameter.Value.(*types.FieldMemberStringValue); ok && value.Value == model.Description {
				plainTextFound = true
			}
			continue
		}
		expectedValue, ok := expected[parameter.TypeHint]
		if !ok {
			t.Errorf("unexpected Data API type hint %q", parameter.TypeHint)
			continue
		}
		value, ok := parameter.Value.(*types.FieldMemberStringValue)
		if !ok {
			t.Errorf("hint %q did not use a string parameter: %#v", parameter.TypeHint, parameter.Value)
			continue
		}
		if value.Value != expectedValue {
			t.Errorf("hint %q value = %q, want %q", parameter.TypeHint, value.Value, expectedValue)
		}
		found[parameter.TypeHint] = true
	}
	if len(found) != len(expected) {
		t.Errorf("expected hints for %v, got %v", expected, found)
	}
	if !plainTextFound {
		t.Error("expected ordinary text to reach the Data API without a type hint")
	}
}

func newTypedRecord() typedRecord {
	date := time.Date(2026, 10, 27, 9, 45, 0, 123000000, time.UTC)
	return typedRecord{
		ID:          1,
		Payload:     `{"mode":"strict"}`,
		Date:        date,
		Amount:      "12345678901234567890.123400",
		TimeOfDay:   date,
		OccurredAt:  date,
		ExternalID:  "550e8400-e29b-41d4-a716-446655440000",
		Description: `{"not":"json typed"}`,
	}
}

func expectedTypeHintValues() map[types.TypeHint]string {
	return map[types.TypeHint]string{
		types.TypeHintJson:      `{"mode":"strict"}`,
		types.TypeHintDate:      "2026-10-27",
		types.TypeHintDecimal:   "12345678901234567890.123400",
		types.TypeHintTime:      "09:45:00.123",
		types.TypeHintTimestamp: "2026-10-27 09:45:00.123",
		types.TypeHintUuid:      "550e8400-e29b-41d4-a716-446655440000",
	}
}

func assertTypeHintArgs(t *testing.T, args []interface{}, expected map[types.TypeHint]string, plainText string) {
	t.Helper()
	found := make(map[types.TypeHint]bool, len(expected))
	plainTextFound := false
	for _, arg := range args {
		hinted, ok := arg.(rds.TypeHinter)
		if !ok {
			if value, ok := arg.(string); ok && value == plainText {
				plainTextFound = true
			}
			continue
		}
		expectedValue, ok := expected[hinted.TypeHint()]
		if !ok {
			t.Errorf("unexpected GORM type hint %q", hinted.TypeHint())
			continue
		}
		value, err := hinted.Value()
		if err != nil {
			t.Errorf("could not read value for type hint %q: %v", hinted.TypeHint(), err)
			continue
		}
		if value != expectedValue {
			t.Errorf("hint %q value = %v, want %q", hinted.TypeHint(), value, expectedValue)
		}
		found[hinted.TypeHint()] = true
	}
	if len(found) != len(expected) {
		t.Errorf("expected hints for %v, got %v", expected, found)
	}
	if !plainTextFound {
		t.Error("expected JSON-looking ordinary text to remain unhinted")
	}
}

func assertJSONAndTextArgs(t *testing.T, args []interface{}, expectedJSON, expectedText string) {
	t.Helper()
	hintedJSONCount := 0
	plainTextFound := false
	for _, arg := range args {
		if hinted, ok := arg.(rds.JSON); ok {
			hintedJSONCount++
			if hinted.TypeHint() != types.TypeHintJson {
				t.Errorf("expected JSON type hint, got %q", hinted.TypeHint())
			}
			serialized, err := hinted.Value()
			if err != nil {
				t.Errorf("could not read JSON marker: %v", err)
			} else if serialized != expectedJSON {
				t.Errorf("unexpected serialized JSON: %v", serialized)
			}
		}
		if text, ok := arg.(string); ok && text == expectedText {
			plainTextFound = true
		}
	}
	if hintedJSONCount != 1 {
		t.Errorf("expected one explicitly hinted JSON value, got %d", hintedJSONCount)
	}
	if !plainTextFound {
		t.Errorf("expected JSON-looking ordinary text %q to remain a string", expectedText)
	}
}

func newTestDB(t *testing.T, pool gorm.ConnPool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(testDialector{pool: pool}, &gorm.Config{
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
	})
	if err != nil {
		t.Fatalf("could not open test GORM DB: %v", err)
	}
	return db
}

type testDialector struct {
	pool gorm.ConnPool
}

func (d testDialector) Name() string {
	return "rds-json-hint-test"
}

func (d testDialector) Initialize(db *gorm.DB) error {
	db.ConnPool = d.pool
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	return nil
}

func (testDialector) Migrator(*gorm.DB) gorm.Migrator {
	return nil
}

func (testDialector) DataTypeOf(field *schema.Field) string {
	return string(field.DataType)
}

func (testDialector) DefaultValueOf(*schema.Field) clause.Expression {
	return clause.Expr{SQL: "DEFAULT"}
}

func (testDialector) BindVarTo(writer clause.Writer, stmt *gorm.Statement, _ interface{}) {
	writer.WriteByte('$')
	writer.WriteString(strconv.Itoa(len(stmt.Vars)))
}

func (testDialector) QuoteTo(writer clause.Writer, value string) {
	writer.WriteByte('"')
	writer.WriteString(value)
	writer.WriteByte('"')
}

func (testDialector) Explain(query string, _ ...interface{}) string {
	return query
}

type captureConnPool struct {
	execArgs [][]interface{}
}

func (*captureConnPool) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errors.New("unexpected PrepareContext call")
}

func (pool *captureConnPool) ExecContext(_ context.Context, _ string, args ...interface{}) (sql.Result, error) {
	pool.execArgs = append(pool.execArgs, append([]interface{}(nil), args...))
	return captureResult{}, nil
}

func (*captureConnPool) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, errors.New("unexpected QueryContext call")
}

func (*captureConnPool) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	panic("unexpected QueryRowContext call")
}

type captureResult struct{}

func (captureResult) LastInsertId() (int64, error) {
	return 1, nil
}

func (captureResult) RowsAffected() (int64, error) {
	return 1, nil
}

type dataAPIConnector struct {
	client rds.AWSClientInterface
	config *rds.Config
}

func (connector dataAPIConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return rds.NewConnection(ctx, connector.client, connector.config, rds.NewPostgres(connector.config)), nil
}

func (dataAPIConnector) Driver() driver.Driver {
	return rds.NewDriver()
}

type captureRDSClient struct {
	statements    []*rdsdata.ExecuteStatementInput
	begins        []*rdsdata.BeginTransactionInput
	transactionID *string
	commits       []*rdsdata.CommitTransactionInput
}

func (client *captureRDSClient) ExecuteStatement(_ context.Context, input *rdsdata.ExecuteStatementInput, _ ...func(*rdsdata.Options)) (*rdsdata.ExecuteStatementOutput, error) {
	client.statements = append(client.statements, input)
	return &rdsdata.ExecuteStatementOutput{NumberOfRecordsUpdated: 1}, nil
}

func (client *captureRDSClient) BeginTransaction(_ context.Context, input *rdsdata.BeginTransactionInput, _ ...func(*rdsdata.Options)) (*rdsdata.BeginTransactionOutput, error) {
	client.begins = append(client.begins, input)
	return &rdsdata.BeginTransactionOutput{TransactionId: client.transactionID}, nil
}

func (client *captureRDSClient) CommitTransaction(_ context.Context, input *rdsdata.CommitTransactionInput, _ ...func(*rdsdata.Options)) (*rdsdata.CommitTransactionOutput, error) {
	client.commits = append(client.commits, input)
	return &rdsdata.CommitTransactionOutput{}, nil
}

func (*captureRDSClient) RollbackTransaction(context.Context, *rdsdata.RollbackTransactionInput, ...func(*rdsdata.Options)) (*rdsdata.RollbackTransactionOutput, error) {
	return &rdsdata.RollbackTransactionOutput{}, nil
}
