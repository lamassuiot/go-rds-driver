package rds_test

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/krotscheck/go-rds-driver"
	. "github.com/smartystreets/goconvey/convey"
)

func Test_Dialect(t *testing.T) {

	Convey("ConvertNamedValue", t, func() {

		Convey("Null Values", func() {
			var UInt8 []uint8
			var UInt8Ptr *[]uint8

			values := []driver.Value{
				UInt8,
				UInt8Ptr,
			}

			for _, v := range values {
				namedValue := driver.NamedValue{
					Name:  "name",
					Value: v,
				}
				result, err := rds.ConvertNamedValue(namedValue)
				So(err, ShouldBeNil)
				So(result, ShouldResemble, types.SqlParameter{
					Name:  aws.String("name"),
					Value: &types.FieldMemberIsNull{Value: true},
				})
			}

		})

		Convey("Plain strings do not receive inferred type hints", func() {
			values := []string{
				`{"mode":"strict"}`,
				"550e8400-e29b-41d4-a716-446655440000",
				"2026-10-27",
				"123.4500",
				"09:45:00",
				"2026-10-27 09:45:00",
			}

			for _, value := range values {
				result, err := rds.ConvertNamedValue(driver.NamedValue{
					Name:  "value",
					Value: value,
				})
				So(err, ShouldBeNil)
				So(result.TypeHint, ShouldBeEmpty)
				So(result.Value, ShouldResemble, &types.FieldMemberStringValue{Value: value})
			}
		})

		Convey("Explicit type-hinted values receive type hints", func() {
			values := []struct {
				value    interface{}
				typeHint types.TypeHint
				text     string
			}{
				{rds.JSON(`{"mode":"strict"}`), types.TypeHintJson, `{"mode":"strict"}`},
				{rds.UUID("550e8400-e29b-41d4-a716-446655440000"), types.TypeHintUuid, "550e8400-e29b-41d4-a716-446655440000"},
				{rds.Date("2026-10-27"), types.TypeHintDate, "2026-10-27"},
				{rds.Decimal("123.4500"), types.TypeHintDecimal, "123.4500"},
				{rds.Time("09:45:00.123"), types.TypeHintTime, "09:45:00.123"},
				{rds.Timestamp("2026-10-27 09:45:00.123"), types.TypeHintTimestamp, "2026-10-27 09:45:00.123"},
			}

			for _, value := range values {
				result, err := rds.ConvertNamedValue(driver.NamedValue{
					Name:  "value",
					Value: value.value,
				})
				So(err, ShouldBeNil)
				So(result.TypeHint, ShouldEqual, value.typeHint)
				So(result.Value, ShouldResemble, &types.FieldMemberStringValue{Value: value.text})
			}
		})

		Convey("time.Time receives the TIMESTAMP type hint", func() {
			value := time.Date(2026, 10, 27, 9, 45, 0, 123000000, time.UTC)
			result, err := rds.ConvertNamedValue(driver.NamedValue{
				Name:  "value",
				Value: value,
			})
			So(err, ShouldBeNil)
			So(result.TypeHint, ShouldEqual, types.TypeHintTimestamp)
			So(result.Value, ShouldResemble, &types.FieldMemberStringValue{Value: "2026-10-27 09:45:00.123"})
		})
	})

	Convey("Connection named value checker preserves typed values", t, func() {
		connection := &rds.Connection{}
		for _, value := range []interface{}{
			rds.JSON(`{"mode":"strict"}`),
			rds.UUID("550e8400-e29b-41d4-a716-446655440000"),
			rds.Date("2026-10-27"),
			rds.Decimal("123.4500"),
			rds.Time("09:45:00"),
			rds.Timestamp("2026-10-27 09:45:00"),
		} {
			namedValue := driver.NamedValue{Value: value}
			So(connection.CheckNamedValue(&namedValue), ShouldBeNil)
		}

		namedValue := driver.NamedValue{Value: `{"mode":"strict"}`}
		So(connection.CheckNamedValue(&namedValue), ShouldEqual, driver.ErrSkip)
	})
}

func TestPostgresDefaultTransactionDoesNotRequireSetupQuery(t *testing.T) {
	dialect := rds.NewPostgres(rds.NewConfig("resource", "secret", "database", "region"))

	if query := dialect.GetTransactionSetupQuery(driver.TxOptions{}); query != "" {
		t.Fatalf("default transaction should not require setup SQL, got %q", query)
	}
}
