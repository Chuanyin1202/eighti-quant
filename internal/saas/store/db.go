package store

import (
	_ "embed"
	"fmt"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed indexes.sql
var indexesSQL string

// DB wraps a *gorm.DB with EightiQuant-specific lifecycle helpers.
type DB struct {
	*gorm.DB
}

// NewDB connects to Postgres, runs AutoMigrate, runs the champion preflight,
// and applies indexes.sql. Any failure aborts startup — no degraded mode.
//
// Startup order (must not be reordered, see CLAUDE.md §2.4 + docs/20- §5.2.3):
//   1. Connect
//   2. AutoMigrate (table structure from struct tags)
//   3. championIndexPreflight (detect existing duplicate champions before
//      creating the unique index — IF NOT EXISTS won't catch existing dupes)
//   4. ApplyIndexes (run indexes.sql; partial unique index installation)
func NewDB(cfg config.DatabaseConfig) (*DB, error) {
	gdb, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		// SaaS workload is mostly small queries on indexed columns; default
		// silent logger is enough. Enable info level via env in dev if needed.
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("store: connect postgres: %w", err)
	}

	if err := gdb.AutoMigrate(AllModels()...); err != nil {
		return nil, fmt.Errorf("store: automigrate: %w", err)
	}

	if err := championIndexPreflight(gdb); err != nil {
		return nil, fmt.Errorf("store: champion preflight: %w", err)
	}

	if err := applyIndexes(gdb); err != nil {
		return nil, fmt.Errorf("store: apply indexes.sql: %w", err)
	}

	return &DB{DB: gdb}, nil
}

// applyIndexes runs the embedded indexes.sql.
//
// Splitting on `;` is sufficient for our schema since none of the DDLs
// contain inline strings or PL/pgSQL bodies. Revisit if that changes.
func applyIndexes(gdb *gorm.DB) error {
	for _, stmt := range splitSQL(indexesSQL) {
		if stmt == "" {
			continue
		}
		if err := gdb.Exec(stmt).Error; err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// splitSQL is a minimal `;` splitter that strips comments and blank lines.
func splitSQL(src string) []string {
	var out []string
	var cur []byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '-' && i+1 < len(src) && src[i+1] == '-' {
			// skip rest of line
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if c == ';' {
			out = append(out, trimSpaceASCII(string(cur)))
			cur = cur[:0]
			continue
		}
		cur = append(cur, c)
	}
	if rest := trimSpaceASCII(string(cur)); rest != "" {
		out = append(out, rest)
	}
	return out
}

func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && isSpaceASCII(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpaceASCII(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpaceASCII(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
