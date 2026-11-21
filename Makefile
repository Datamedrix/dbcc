# dbcc build and test targets.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DATE    := $(shell date -u +%Y-%m-%d)
LDFLAGS := -s -w -X dbcc/internal/branding.Version=$(VERSION) -X dbcc/internal/branding.BuildDate=$(DATE)

export DBCC_IT_MYSQL   ?= 127.0.0.1:13306
export DBCC_IT_MARIADB ?= 127.0.0.1:13307
export DBCC_IT_PW      ?= dbcctest
export DBCC_IT_POSTGRES ?= 127.0.0.1:15432
export DBCC_IT_PG_PW    ?= dbcctest
export DBCC_IT_MSSQL    ?= 127.0.0.1:11433
export DBCC_IT_MSSQL_PW ?= Dbcc-test-2026

.PHONY: build test itest static-check test-servers licenses
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dbcc ./cmd/dbcc

test:
	go test ./...

# Licence notices of the compiled-in modules (spec 7.1, A16). Rerun after
# every go.mod change; a test fails while the file is stale.
licenses:
	go run ./internal/branding/gen/licenses -o internal/branding/THIRD_PARTY_LICENSES.txt

itest:
	go test -tags integration -count=1 ./...

# CI check: the binary must be statically linked (spec R1).
static-check: build
	@file bin/dbcc | grep -q 'statically linked' && echo "bin/dbcc is statically linked" || (file bin/dbcc; exit 1)

# Development servers in containers (spec 16, Testing).
test-servers:
	docker start dbcc-test-mysql 2>/dev/null || docker run -d --name dbcc-test-mysql -e MYSQL_ROOT_PASSWORD=$(DBCC_IT_PW) -p 127.0.0.1:13306:3306 mysql:8.4
	docker start dbcc-test-mariadb 2>/dev/null || docker run -d --name dbcc-test-mariadb -e MARIADB_ROOT_PASSWORD=$(DBCC_IT_PW) -p 127.0.0.1:13307:3306 mariadb:11
	docker start dbcc-test-postgres 2>/dev/null || docker run -d --name dbcc-test-postgres -e POSTGRES_PASSWORD=$(DBCC_IT_PG_PW) -p 127.0.0.1:15432:5432 postgres:17
	docker start dbcc-test-mssql 2>/dev/null || docker run -d --name dbcc-test-mssql -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=$(DBCC_IT_MSSQL_PW) -p 127.0.0.1:11433:1433 mcr.microsoft.com/mssql/server:2022-latest
