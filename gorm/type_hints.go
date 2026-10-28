package gorm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	rds "github.com/krotscheck/go-rds-driver"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// TypeHintPlugin adds explicit RDS Data API type hints to GORM operations for
// fields whose schema declares a supported SQL type.
type TypeHintPlugin struct{}

// NewTypeHintPlugin returns a plugin for a GORM connection using the RDS Data API driver.
func NewTypeHintPlugin() *TypeHintPlugin {
	return &TypeHintPlugin{}
}

// Name identifies this GORM plugin.
func (*TypeHintPlugin) Name() string {
	return "rds:type_hints"
}

// Initialize installs callbacks on this GORM DB instance only.
func (*TypeHintPlugin) Initialize(db *gorm.DB) error {
	create := db.Callback().Create()
	if err := create.Before("gorm:create").Register("rds:type_hints:create:begin", beginTypeHintOperation(createOperation)); err != nil {
		return err
	}
	if err := create.Before("gorm:commit_or_rollback_transaction").Register("rds:type_hints:create:end", endTypeHintOperation); err != nil {
		return err
	}

	update := db.Callback().Update()
	if err := update.Before("gorm:update").Register("rds:type_hints:update:begin", beginTypeHintOperation(updateOperation)); err != nil {
		return err
	}
	if err := update.Before("gorm:commit_or_rollback_transaction").Register("rds:type_hints:update:end", endTypeHintOperation); err != nil {
		return err
	}

	query := db.Callback().Query()
	if err := query.Before("gorm:query").Register("rds:type_hints:query:where", hintWhereClauseTypeHints); err != nil {
		return err
	}

	deleteCallback := db.Callback().Delete()
	if err := deleteCallback.Before("gorm:delete").Register("rds:type_hints:delete:where", hintWhereClauseTypeHints); err != nil {
		return err
	}

	return nil
}

type operation uint8

const (
	createOperation operation = iota
	updateOperation
)

type hintingConnPool struct {
	gorm.ConnPool
	statement *gorm.Statement
	operation operation
}

func beginTypeHintOperation(op operation) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement == nil || tx.Statement.ConnPool == nil || tx.Statement.Schema == nil {
			return
		}

		if op == updateOperation {
			hintWhereClauseTypeHints(tx)
			if tx.Error != nil {
				return
			}
		}

		tx.Statement.ConnPool = &hintingConnPool{
			ConnPool:  tx.Statement.ConnPool,
			statement: tx.Statement,
			operation: op,
		}
	}
}

func hintWhereClauseTypeHints(tx *gorm.DB) {
	if tx.Error != nil || tx.Statement == nil || tx.Statement.Schema == nil {
		return
	}

	whereClause, ok := tx.Statement.Clauses["WHERE"]
	if !ok {
		return
	}
	where, ok := whereClause.Expression.(clause.Where)
	if !ok {
		return
	}

	for i, expression := range where.Exprs {
		equality, ok := expression.(clause.Eq)
		if !ok {
			continue
		}

		columnName := ""
		switch column := equality.Column.(type) {
		case clause.Column:
			columnName = column.Name
		case string:
			columnName = column
		default:
			continue
		}

		field := tx.Statement.Schema.LookUpField(columnName)
		typeHint := fieldTypeHint(field)
		if typeHint == "" {
			continue
		}

		value, err := typeHintValue(typeHint, equality.Value)
		if err != nil {
			tx.AddError(fmt.Errorf("RDS type hint %s for WHERE field %q: %w", typeHint, columnName, err))
			return
		}
		equality.Value = value
		where.Exprs[i] = equality
	}

	whereClause.Expression = where
	tx.Statement.Clauses["WHERE"] = whereClause
}

func endTypeHintOperation(tx *gorm.DB) {
	if tx.Statement == nil {
		return
	}
	if pool, ok := tx.Statement.ConnPool.(*hintingConnPool); ok {
		tx.Statement.ConnPool = pool.ConnPool
	}
}

func (pool *hintingConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return pool.ConnPool.PrepareContext(ctx, query)
}

func (pool *hintingConnPool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	args, err := pool.withTypeHints(args)
	if err != nil {
		return nil, err
	}
	return pool.ConnPool.ExecContext(ctx, query, args...)
}

func (pool *hintingConnPool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	args, err := pool.withTypeHints(args)
	if err != nil {
		return nil, err
	}
	return pool.ConnPool.QueryContext(ctx, query, args...)
}

func (pool *hintingConnPool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return pool.ConnPool.QueryRowContext(ctx, query, args...)
}

func (pool *hintingConnPool) withTypeHints(args []interface{}) ([]interface{}, error) {
	switch pool.operation {
	case createOperation:
		return pool.createArgsWithTypeHints(args)
	case updateOperation:
		return pool.updateArgsWithTypeHints(args)
	default:
		return args, nil
	}
}

type createCell struct {
	value    interface{}
	typeHint types.TypeHint
}

func (pool *hintingConnPool) createArgsWithTypeHints(args []interface{}) ([]interface{}, error) {
	valuesClause, ok := pool.statement.Clauses["VALUES"]
	if !ok {
		return args, nil
	}
	values, ok := valuesClause.Expression.(clause.Values)
	if !ok {
		return args, nil
	}

	cells := make([]createCell, 0)
	for _, row := range values.Values {
		for i, value := range row {
			if i >= len(values.Columns) {
				return nil, fmt.Errorf("RDS type hint: create row has more values than columns")
			}
			field := pool.statement.Schema.LookUpField(values.Columns[i].Name)
			cells = append(cells, createCell{
				value:    value,
				typeHint: fieldTypeHint(field),
			})
		}
	}

	return applyTypeHints(args, cells)
}

func (pool *hintingConnPool) updateArgsWithTypeHints(args []interface{}) ([]interface{}, error) {
	setClause, ok := pool.statement.Clauses["SET"]
	if !ok {
		return args, nil
	}
	assignments, ok := setClause.Expression.(clause.Set)
	if !ok {
		return args, nil
	}

	cells := make([]createCell, 0, len(assignments))
	for _, assignment := range assignments {
		field := pool.statement.Schema.LookUpField(assignment.Column.Name)
		cells = append(cells, createCell{
			value:    assignment.Value,
			typeHint: fieldTypeHint(field),
		})
	}
	return applyTypeHints(args, cells)
}

func applyTypeHints(args []interface{}, cells []createCell) ([]interface{}, error) {
	hasHint := false
	for _, cell := range cells {
		hasHint = hasHint || cell.typeHint != ""
	}
	if !hasHint {
		return args, nil
	}

	hasLaterHint := make([]bool, len(cells))
	nextHint := false
	for i := len(cells) - 1; i >= 0; i-- {
		hasLaterHint[i] = nextHint
		nextHint = nextHint || cells[i].typeHint != ""
	}

	argIndex := 0
	for i := range cells {
		bindCount := 1
		if cells[i].typeHint != "" || hasLaterHint[i] {
			var err error
			bindCount, err = bindVarCount(cells[i].value)
			if err != nil {
				return nil, err
			}
		}

		if cells[i].typeHint != "" && bindCount > 0 {
			if argIndex >= len(args) {
				return nil, fmt.Errorf("RDS type hint %s: no bound parameter for field", cells[i].typeHint)
			}
			value, err := typeHintValue(cells[i].typeHint, args[argIndex])
			if err != nil {
				return nil, fmt.Errorf("RDS type hint %s: %w", cells[i].typeHint, err)
			}
			args[argIndex] = value
		}

		if hasLaterHint[i] {
			argIndex += bindCount
		}
	}
	return args, nil
}

func fieldTypeHint(field *schema.Field) types.TypeHint {
	if field == nil {
		return ""
	}
	if strings.EqualFold(field.TagSettings["SERIALIZER"], "json") {
		return types.TypeHintJson
	}

	declaredType := strings.ToLower(strings.TrimSpace(field.TagSettings["TYPE"]))
	if index := strings.IndexByte(declaredType, '('); index >= 0 {
		declaredType = declaredType[:index]
	}
	parts := strings.Fields(declaredType)
	if len(parts) == 0 {
		return ""
	}

	switch parts[0] {
	case "date":
		return types.TypeHintDate
	case "decimal", "numeric":
		return types.TypeHintDecimal
	case "json", "jsonb":
		return types.TypeHintJson
	case "time", "timetz":
		return types.TypeHintTime
	case "timestamp", "timestamptz", "datetime":
		return types.TypeHintTimestamp
	case "uuid":
		return types.TypeHintUuid
	default:
		return ""
	}
}

func typeHintValue(typeHint types.TypeHint, value interface{}) (interface{}, error) {
	if hinted, ok := value.(rds.TypeHinter); ok {
		if hinted.TypeHint() == typeHint {
			return value, nil
		}
		return nil, fmt.Errorf("value already requests incompatible type hint %s", hinted.TypeHint())
	}
	if typeHint == types.TypeHintJson {
		return jsonHintValue(value)
	}
	if value == nil {
		return nil, nil
	}

	if valuer, ok := value.(driver.Valuer); ok {
		converted, err := valuer.Value()
		if err != nil {
			return nil, err
		}
		if converted == nil {
			return nil, nil
		}
		value = converted
	}

	text, err := stringForTypeHint(typeHint, value)
	if err != nil {
		return nil, err
	}
	switch typeHint {
	case types.TypeHintDate:
		return rds.Date(text), nil
	case types.TypeHintDecimal:
		return rds.Decimal(text), nil
	case types.TypeHintTime:
		return rds.Time(text), nil
	case types.TypeHintTimestamp:
		return rds.Timestamp(text), nil
	case types.TypeHintUuid:
		return rds.UUID(text), nil
	default:
		return nil, fmt.Errorf("unsupported RDS Data API type hint %s", typeHint)
	}
}

func stringForTypeHint(typeHint types.TypeHint, value interface{}) (string, error) {
	if timestamp, ok := value.(time.Time); ok {
		switch typeHint {
		case types.TypeHintDate:
			return timestamp.Format("2006-01-02"), nil
		case types.TypeHintTime:
			return timestamp.Format("15:04:05.999"), nil
		case types.TypeHintTimestamp:
			return timestamp.Format("2006-01-02 15:04:05.999"), nil
		default:
			return "", fmt.Errorf("cannot convert time.Time to %s", typeHint)
		}
	}

	switch value := value.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	case int:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatInt(int64(value), 10), nil
		}
	case int8:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatInt(int64(value), 10), nil
		}
	case int16:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatInt(int64(value), 10), nil
		}
	case int32:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatInt(int64(value), 10), nil
		}
	case int64:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatInt(value, 10), nil
		}
	case uint:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatUint(uint64(value), 10), nil
		}
	case uint8:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatUint(uint64(value), 10), nil
		}
	case uint16:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatUint(uint64(value), 10), nil
		}
	case uint32:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatUint(uint64(value), 10), nil
		}
	case uint64:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatUint(value, 10), nil
		}
	case float32:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatFloat(float64(value), 'f', -1, 32), nil
		}
	case float64:
		if typeHint == types.TypeHintDecimal {
			return strconv.FormatFloat(value, 'f', -1, 64), nil
		}
	}
	return "", fmt.Errorf("cannot convert %T to a string for this hint", value)
}

func jsonHintValue(value interface{}) (interface{}, error) {
	if hinted, ok := value.(rds.TypeHinter); ok && hinted.TypeHint() == types.TypeHintJson {
		return value, nil
	}
	switch value := value.(type) {
	case string:
		return rds.JSON(value), nil
	case []byte:
		return rds.JSON(string(value)), nil
	}

	valuer, ok := value.(driver.Valuer)
	if ok {
		serialized, err := valuer.Value()
		if err != nil {
			return nil, err
		}
		if serialized == nil {
			return nil, nil
		}

		switch serialized := serialized.(type) {
		case string:
			return rds.JSON(serialized), nil
		case []byte:
			return rds.JSON(string(serialized)), nil
		default:
			return nil, fmt.Errorf("JSON serializer returned unsupported value %T", serialized)
		}
	}

	if marshaler, ok := value.(json.Marshaler); ok {
		serialized, err := marshaler.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal JSON value %T: %w", value, err)
		}
		return rds.JSON(string(serialized)), nil
	}

	return nil, fmt.Errorf("JSON-serialized field value %T does not implement driver.Valuer or json.Marshaler", value)
}

func bindVarCount(value interface{}) (int, error) {
	switch value := value.(type) {
	case clause.Column, clause.Table:
		return 0, nil
	case clause.Expr:
		return exprBindVarCount(value)
	case clause.NamedExpr:
		return 0, fmt.Errorf("RDS type hint: cannot safely map a hinted field after a named expression")
	case []byte:
		return 1, nil
	case []interface{}:
		count := 0
		for _, item := range value {
			itemCount, err := bindVarCount(item)
			if err != nil {
				return 0, err
			}
			count += itemCount
		}
		return count, nil
	}

	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 1, nil
	}
	if reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array {
		if reflected.Len() == 0 {
			return 0, nil
		}
		if reflected.Type().Elem().Kind() == reflect.Uint8 {
			return 1, nil
		}
		count := 0
		for i := 0; i < reflected.Len(); i++ {
			itemCount, err := bindVarCount(reflected.Index(i).Interface())
			if err != nil {
				return 0, err
			}
			count += itemCount
		}
		return count, nil
	}
	return 1, nil
}

func exprBindVarCount(expr clause.Expr) (int, error) {
	count := 0
	index := 0
	afterParenthesis := false
	for _, char := range []byte(expr.SQL) {
		if char == '?' && index < len(expr.Vars) {
			value := expr.Vars[index]
			if afterParenthesis || expr.WithoutParentheses {
				if _, ok := value.(driver.Valuer); !ok {
					reflected := reflect.ValueOf(value)
					if reflected.IsValid() && (reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array) {
						if reflected.Len() == 0 {
							count++
						} else {
							for i := 0; i < reflected.Len(); i++ {
								itemCount, err := bindVarCount(reflected.Index(i).Interface())
								if err != nil {
									return 0, err
								}
								count += itemCount
							}
						}
					} else {
						count++
					}
				} else {
					count++
				}
			} else {
				count++
			}
			index++
		} else if char == '(' {
			afterParenthesis = true
		} else {
			afterParenthesis = false
		}
	}
	count += len(expr.Vars) - index
	return count, nil
}
