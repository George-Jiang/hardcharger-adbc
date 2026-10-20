// Licensed under the Apache License, Version 2.0.

// Query Hardcharger from Go with the driver package itself (no driver manager).
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/gizmodata/hardcharger-adbc/go/v2/hardcharger"
)

func main() {
	ctx := context.Background()
	db, err := hardcharger.NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, map[string]string{
		"uri":                      "hardcharger://localhost?tls=false",
		"username":                 "hardcharger",
		"password":                 "change-me",
		hardcharger.OptionQueryTag: `{"app": "hello.go"}`,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close(ctx)
	cnxn, err := db.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cnxn.Close(ctx)

	stmt, err := cnxn.NewStatement(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close(ctx)
	if err := stmt.SetSqlQuery(ctx, "SELECT 42 AS answer"); err != nil {
		log.Fatal(err)
	}
	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Release()
	for reader.Next() {
		fmt.Println(reader.RecordBatch())
	}
	if err := reader.Err(); err != nil {
		log.Fatal(err)
	}
}
