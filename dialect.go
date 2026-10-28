package rds

import (
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
)

// FieldConverter is a function that converts the passed result row field into the expected type.
type FieldConverter func(field types.Field) (interface{}, error)

// TypeHinter is implemented by values that explicitly provide an RDS Data API type hint.
type TypeHinter interface {
	driver.Valuer
	TypeHint() types.TypeHint
}

// JSON is a string value that explicitly requests the RDS Data API JSON type hint.
type JSON string

// Value implements driver.Valuer.
func (value JSON) Value() (driver.Value, error) {
	return string(value), nil
}

// TypeHint returns the RDS Data API JSON type hint.
func (JSON) TypeHint() types.TypeHint {
	return types.TypeHintJson
}

// UUID is a string value that explicitly requests the RDS Data API UUID type hint.
type UUID string

// Value implements driver.Valuer.
func (value UUID) Value() (driver.Value, error) {
	return string(value), nil
}

// TypeHint returns the RDS Data API UUID type hint.
func (UUID) TypeHint() types.TypeHint {
	return types.TypeHintUuid
}

// Date is a string value that explicitly requests the RDS Data API DATE type hint.
type Date string

// Value implements driver.Valuer.
func (value Date) Value() (driver.Value, error) {
	return string(value), nil
}

// TypeHint returns the RDS Data API DATE type hint.
func (Date) TypeHint() types.TypeHint {
	return types.TypeHintDate
}

// Decimal is a string value that explicitly requests the RDS Data API DECIMAL type hint.
type Decimal string

// Value implements driver.Valuer.
func (value Decimal) Value() (driver.Value, error) {
	return string(value), nil
}

// TypeHint returns the RDS Data API DECIMAL type hint.
func (Decimal) TypeHint() types.TypeHint {
	return types.TypeHintDecimal
}

// Time is a string value that explicitly requests the RDS Data API TIME type hint.
type Time string

// Value implements driver.Valuer.
func (value Time) Value() (driver.Value, error) {
	return string(value), nil
}

// TypeHint returns the RDS Data API TIME type hint.
func (Time) TypeHint() types.TypeHint {
	return types.TypeHintTime
}

// Timestamp is a string value that explicitly requests the RDS Data API TIMESTAMP type hint.
type Timestamp string

// Value implements driver.Valuer.
func (value Timestamp) Value() (driver.Value, error) {
	return string(value), nil
}

// TypeHint returns the RDS Data API TIMESTAMP type hint.
func (Timestamp) TypeHint() types.TypeHint {
	return types.TypeHintTimestamp
}

// Dialect is an interface that encapsulates a particular languages' eccentricities
type Dialect interface {
	// MigrateQuery from the dialect to RDS
	MigrateQuery(string, []driver.NamedValue) (*rdsdata.ExecuteStatementInput, error)
	// GetFieldConverter for a given ColumnMetadata.TypeName field.
	GetFieldConverter(columnType string) FieldConverter
	// IsIsolationLevelSupported for this dialect?
	IsIsolationLevelSupported(level driver.IsolationLevel) bool
	// GetTransactionSetupQuery returns the query to set up the transaction.
	GetTransactionSetupQuery(opts driver.TxOptions) string
}

// ConvertNamedValues converts passed driver.NamedValue instances into RDS SQLParameters
func ConvertNamedValues(args []driver.NamedValue) ([]types.SqlParameter, error) {
	var params = make([]types.SqlParameter, len(args))
	for i, arg := range args {
		sqlParam, err := ConvertNamedValue(arg)
		if err != nil {
			return nil, err
		}
		params[i] = sqlParam
	}
	return params, nil
}

// ConvertNamedValue from a NamedValue to an SqlParameter
func ConvertNamedValue(arg driver.NamedValue) (value types.SqlParameter, err error) {
	name := arg.Name

	if isNil(arg.Value) {
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberIsNull{Value: true},
		}
		return
	}
	var typeHint types.TypeHint
	if hinted, ok := arg.Value.(TypeHinter); ok {
		typeHint = hinted.TypeHint()
		arg.Value, err = hinted.Value()
		if err != nil {
			return
		}
		if isNil(arg.Value) {
			value = types.SqlParameter{
				Name:     &name,
				TypeHint: typeHint,
				Value:    &types.FieldMemberIsNull{Value: true},
			}
			return
		}
	}
	switch t := arg.Value.(type) {
	case string:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberStringValue{Value: t},
		}
	case []byte:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberBlobValue{Value: t},
		}
	case bool:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberBooleanValue{Value: t},
		}
	case float32:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberDoubleValue{Value: float64(t)},
		}
	case float64:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberDoubleValue{Value: float64(t)},
		}
	case int:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case int8:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case int16:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case int32:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case int64:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: t},
		}
	case uint:
		v := uint64(t)
		if v > math.MaxInt64 {
			err = fmt.Errorf("%s value %d overflows int64", name, t)
			return
		}
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(v)},
		}
	case uint8:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberBlobValue{Value: []byte{t}},
		}
	case uint16:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case uint32:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case uint64:
		if uint64(t) > math.MaxInt64 {
			err = fmt.Errorf("%s value %d overflows int64", name, t)
			return
		}
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberLongValue{Value: int64(t)},
		}
	case time.Time:
		value = types.SqlParameter{
			Name:     &name,
			TypeHint: types.TypeHintTimestamp,
			Value: &types.FieldMemberStringValue{
				Value: t.Format("2006-01-02 15:04:05.999"),
			},
		}
	case nil:
		value = types.SqlParameter{
			Name:  &name,
			Value: &types.FieldMemberIsNull{Value: true},
		}
	default:
		err = fmt.Errorf("%s is unsupported type: %#v", name, arg.Value)
		return
	}
	if typeHint != "" {
		value.TypeHint = typeHint
	}
	return
}

func isNil(i interface{}) bool {
	if i == nil {
		return true
	}
	switch reflect.TypeOf(i).Kind() {
	case reflect.Ptr, reflect.Map, reflect.Array, reflect.Chan, reflect.Slice:
		return reflect.ValueOf(i).IsNil()
	}
	return false
}

// convertJSONField returns bytes so database/sql can scan JSON into named byte-slice types.
func convertJSONField(field types.Field) (interface{}, error) {
	return []byte(field.(*types.FieldMemberStringValue).Value), nil
}

// ConvertDefaults handles all types that can be returned directly without additional conversion.
func ConvertDefaults() func(field types.Field) (value interface{}, err error) {
	// For unknown field types (like custom ENUM types) we fall back to the
	// value type of the field.
	return func(field types.Field) (value interface{}, err error) {
		switch v := field.(type) {
		case *types.FieldMemberArrayValue:
			value = v.Value
		case *types.FieldMemberBlobValue:
			value = v.Value
		case *types.FieldMemberBooleanValue:
			value = v.Value
		case *types.FieldMemberDoubleValue:
			value = v.Value
		case *types.FieldMemberLongValue:
			value = v.Value
		case *types.FieldMemberStringValue:
			value = v.Value
		case *types.FieldMemberIsNull:
			return nil, nil
		default:
			return nil, fmt.Errorf("Unrecognized RDS field type: %#v", field)
		}

		return
	}
}
