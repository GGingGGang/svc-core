package migrations

import (
	"io/fs"
	"os"
	"testing"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

func TestMigrationsIncludedInDockerContext(t *testing.T) {
	f, err := os.Open("../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	patterns, err := ignorefile.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(Files, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"db/migrate.go"}
	for _, file := range files {
		paths = append(paths, "db/"+file)
	}
	for _, path := range paths {
		excluded, err := matcher.MatchesOrParentMatches(path)
		if err != nil {
			t.Fatal(err)
		}
		if excluded {
			t.Errorf(".dockerignore excludes required migration file %s", path)
		}
	}
}
