// Command cleanup-extractions clears retained AI input/output content before
// an explicit RFC3339 cutoff. Without --apply it only reports the row count.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/GGingGGang/svc-core/internal/config"
	"github.com/GGingGGang/svc-core/internal/db"
)

func main() {
	beforeArg := flag.String("before", "", "required RFC3339 cutoff; only rows created before this instant are targeted")
	apply := flag.Bool("apply", false, "clear raw input and output; without this flag only count affected rows")
	flag.Parse()
	if *beforeArg == "" || flag.NArg() != 0 {
		log.Fatal("usage: cleanup-extractions --before RFC3339 [--apply]")
	}
	before, err := time.Parse(time.RFC3339, *beforeArg)
	if err != nil {
		log.Fatal("--before must be RFC3339")
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("load database configuration failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sqlDB, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatal("connect database failed")
	}
	defer sqlDB.Close()
	var count int64
	err = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_extractions
WHERE created_at < ? AND (raw_text IS NOT NULL OR result_json IS NOT NULL)`, before.UTC()).Scan(&count)
	if err != nil {
		log.Fatal("count targeted extraction rows failed")
	}
	if !*apply {
		fmt.Printf("dry-run: %d extraction rows before %s contain retained content; rerun with --apply to clear it\n", count, before.UTC().Format(time.RFC3339))
		return
	}
	result, err := sqlDB.ExecContext(ctx, `UPDATE ai_extractions SET raw_text = NULL, result_json = NULL
WHERE created_at < ? AND (raw_text IS NOT NULL OR result_json IS NOT NULL)`, before.UTC())
	if err != nil {
		log.Fatal("clear extraction content failed")
	}
	changed, err := result.RowsAffected()
	if err != nil {
		log.Fatal("read cleanup result failed")
	}
	fmt.Printf("cleared retained content from %d extraction rows before %s\n", changed, before.UTC().Format(time.RFC3339))
}
