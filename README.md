# go-rds-driver
A golang sql Driver for the Amazon Aurora Serverless data api.

> **Note:** The serverless data api only supports _named_ query parameters, not ordinal ones. We
> perform a simple ordinal variable replacement in the driver, however we strongly recommend
> you used named parameters as a general rule.

## Table of Contents

* [go-rds-driver](#go-rds-driver)
  * [Table of Contents](#table-of-contents)
  * [Getting Started](#getting-started)
  * [Supported Databases](#supported-databases)
  * [Data Mappings](#data-mappings)
    * [MySQL](#mysql)
    * [PostgreSQL](#postgresql)
  * [Options](#options)
  * [Using your own RDS Client](#using-your-own-rds-client)
  * [Usage with Gorm](#usage-with-gorm)
  * [Running the tests](#running-the-tests)
    * [Creating locally run test databases](#creating-locally-run-test-databases)
    * [Creating RDS Test databases](#creating-rds-test-databases)
    * [Executing checks](#executing-checks)
  * [Contributing](#contributing)
  * [Why does this even exist?](#why-does-this-even-exist)
  * [Acknowledgments](#acknowledgments)
  * [License](#license)

## Getting Started

The `dsn` used in this driver follows the standard URL pattern, however given the complexity
of the ARN input parameters required and reserved URI characters, we're forced to put
all parameters - even the required ones - in the query parameters.
```text
rds://?resource_arn=...&secret_arn=...&database=...&aws_region=...
```
This complex string may be generated using the `Config` type and its `ToDSN` method.

```go
conf := &rds.Config{
    ResourceArn: "...",
    SecretArn:   "...",
    Database:    "...",
    AWSRegion:   "...",
	SplitMulti:  false,
	ParseTime:   true,
}
dsn, err := conf.ToDSN()

db.ConnPool, err = sql.Open(rds.DRIVERNAME, dsn)
```

## Supported Databases

This driver supports the following databases:

*   MySQL 5.7
*   PostgreSQL 10.14

## Data Mappings

The nature of our data translation - from DB to HTTP to Go - makes converting database types somewhat tricky. In most cases, we've done our best to match the behavior of a commonly used driver, so swapping from Data API to Driver can be done quickly and easily. Even so, there are some unusual behaviors of the RDS Data API that we call out below:

### MySQL

The RDS MySQL version supported is 5.7. Driver parity is tested using `github.com/go-sql-driver/mysql`.

| Column Type | RDS Data API Behavior                                                                                                                                                           |
| :---------- | :------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Unsigned Int| Not natively supported by the AWS SDK's Data API, and are all converted to the int64 type. As such large integer values may be lossy.                                           |
| `BIT(M)`    | The `BIT` column type is returned from RDS as a Boolean, preventing the full use of `BIT(M)`. Until (if ever) this is fixed, only `BIT(1)` column values are supported.          |
| `TINYINT(1)`| Declaring a `TINYINT(1)` in your table will cause the Data API to return a Boolean instead of an integer. Numeric values are only returned by `TINYINT(2)` or greater.             |
| `BOOLEAN`   | The `BOOLEAN` column type is converted into a `BIT` column by RDS.                                                                                                              |
| Booleans    | Boolean marshalling and unmarshalling via `sql.*`, because of the above issues, only works reliably with the `TINYINT(2)` column type. Do not use `BOOLEAN`, `BIT`, or `TINYINT(1)`. |

**Note:** A recent bug fix addresses transaction isolation levels in MySQL.

### PostgreSQL

The RDS Postgres version supported is 13.12. Driver parity is tested using `github.com/jackc/pgx/v4`.

| Feature          | Limitation                                                                                                                              |
| :--------------- | :-------------------------------------------------------------------------------------------------------------------------------------- |
| Unsigned Int     | Not natively supported by the AWS SDK's Data API, and are all converted to the int64 type. As such large integer values may be lossy.      |
| `TIMESTAMPTZ`    | The RDS Data API [always returns `TIMESTAMPTZ` values converted to UTC](https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/data-api-operations.html), regardless of the original timezone. |
| Complex Types    | Postgres complex types - in short anything in [section 8.8](https://www.postgresql.org/docs/10/datatype.html) and after, is not supported. |

### Explicit parameter type hints

The Data API requires type hints for some string parameters, and the driver cannot determine a
string's intended database type from its contents. Plain strings are therefore sent without a
hint, even when they look like JSON, a UUID, a date, or a decimal. Use the corresponding explicit
value type to request a hint:

```go
db.ExecContext(ctx, `INSERT INTO example (metadata, id, description) VALUES ($1, $2, $3)`,
	rds.JSON(`{"mode":"strict"}`),
	rds.UUID("550e8400-e29b-41d4-a716-446655440000"),
	`{"this":"is text"}`,
)

db.ExecContext(ctx, `INSERT INTO measurements (day, amount, starts_at, recorded_at) VALUES ($1, $2, $3, $4)`,
	rds.Date("2026-10-27"),
	rds.Decimal("12345678901234567890.123400"),
	rds.Time("09:45:00.123"),
	rds.Timestamp("2026-10-27 09:45:00.123"),
)
```

`DATE` values use `YYYY-MM-DD`; `TIME` uses `HH:MM:SS[.FFF]`; `TIMESTAMP` uses
`YYYY-MM-DD HH:MM:SS[.FFF]`. `DECIMAL` values are sent as strings to preserve their precision.

## Options
This driver supports a variety of configuration options in the DSN, as follows:

* `parse_time`: Instead of returning the default `string` value of a date or time type,
  the driver will convert it into `time.Time`
* `split_multi`: This option will automatically split all SQL statements by the default
  delimiter `;` and submit them to the API as separate requests. Enable this
  for uses with large migration statements.

## Using your own RDS Client

golang's sql package interfaces provide a challenge, as it's quite difficult to capture all the configuration options
available in an AWS Configuration instance in a DSN. For this, please construct your own client and create a Connector,
then use that connector with the `sql.OpenDB()` method.

```
rdsDriver := rds.NewDriver()
rdsConfig := rds.NewConfig(...)
rdsAWSClient := rdsdata.NewFromConfig(...)
rdsConnector := rds.NewConnector(rdsDriver, rdsAWSClient, rdsConfig)

db := sql.OpenDB(rdsConnector)
```

## Usage with Gorm

The above caveat with the Serverless Data API makes usage of gorm tricky. While you can easily use named parameters
in your own code, the current implementation of the Gorm Migrators (as of Aug 1, 2021) exclusively uses ordinal
parameters. Please be careful when using this driver.

```go
// RDS using the MySQL Dialector
conf := mysql.Config{
    DriverName: rds.DRIVERNAME,
    DSN:        dsn,
}
dialector := mysql.New(conf)


// RDS using the Postgres Dialector
conf := postgres.Config{
    DriverName: rds.DRIVERNAME,
    DSN:        dsn,
}
dialector := postgres.New(conf)
```

Install the optional type-hint plugin on every GORM connection that uses this driver. It uses
existing schema metadata to add hints to supported values across GORM create, read, update, and
delete operations:

- `Create` values and `Update` assignments for fields declared with supported database types, such
  as `type:date`, `type:decimal(30,6)`, `type:time`, `type:timestamp`, and `type:uuid`.
- Structured equality (`=`) predicates in reads, updates, and deletes when GORM can associate the
  predicate with a schema field.
- `serializer:json` fields, without changing their Go types or serializer tags.

The plugin does not infer hints from values or arbitrary SQL. Raw SQL and predicate forms that do
not expose schema field metadata need an explicit value type such as `rds.UUID(value)`, or an SQL
cast where appropriate.

```go
db, err := gorm.Open(postgres.New(postgres.Config{
	DriverName: rds.DRIVERNAME,
	DSN:        dsn,
}), &gorm.Config{})
if err != nil {
	return err
}

if err := db.Use(rdsgorm.NewTypeHintPlugin()); err != nil {
	return err
}
```

Import the plugin as `rdsgorm "github.com/krotscheck/go-rds-driver/gorm"`. Register it only on
GORM connections using the RDS Data API driver. Other GORM connections keep their existing
serialization behavior.

## Running the tests

The intent of this driver is to reach type conversion parity with a database instance that's directly available-
in other words, that the types returned from the respective drivers are identical. For that purpose
we require that you provision a locally run instance of mysql and postgres, as well as an RDS instance of each.
The outputs of each are compared during a test run.

### Creating locally run test databases
Locally run databases can be started using `docker compose up`.

### Creating RDS Test databases
This project includes a `./terraform/` directory which
provisions the necessary resources on RDS. To create them:

```shell
// Choose an AWS profile
export AWS_PROFILE=your_aws_profile_name

cd ./terraform
terraform init
terraform apply
```

To dispose of these resources once you're done:
```shell
cd ./terraform
terraform destroy
```

Once created, the output values will be defined in a local file named `terraform.tfstate`. These
are parsed by our makefile to ensure the correct values are used in the test suite, however
for your local IDE it might be useful to set them directly:

```shell
export AWS_PROFILE = "your_aws_profile"
export RDS_MYSQL_DB_NAME = "go_rds_driver_mysql"
export RDS_MYSQL_ARN = "arn:aws:rds:us-west-2:1234567890:cluster:mysql"
export RDS_POSTGRES_DB_NAME = "go_rds_driver_postgresql"
export RDS_POSTGRES_ARN = "arn:aws:rds:us-west-2:1234567890:cluster:postgresql"
export RDS_SECRET_ARN = "arn:aws:secretsmanager:us-west-2:1234567890:secret:aurora_password"
export AWS_REGION=us-west-2
```

### Executing checks
Executing tests and generating reports can be done via the provided makefile. The makefile now uses `go tool` for its operations, so you don't need to install any of the dependencies manually.
```shell
make clean checks
```

## Contributing

Contributions are welcome! Please feel free to submit a pull request.

Before submitting a pull request, please ensure that your changes are well-tested and that you have updated the documentation if necessary.

## Why does this even exist?

Not everyone has the capital to pay for the VPC resources necessary to access Aurora Serverless directly. In the
author's case, he likes to keep his personal projects as cheap as possible, and paying for all VPC service gateways,
just so he can access an RDBMS, crossed the threshold of affordability. If you're looking to run a personal project
and don't want to break the bank with "overhead" expenses such as VPN service mappings, this driver's the way to go.

## Acknowledgments
This implementation inspired by [what came before](https://github.com/graveyard/rds/tree/birthday).

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.