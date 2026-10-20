// retrieval-eval runs the synthetic, offline character-bigram baseline only.
package main

import (
	"agentos.local/core/internal/retrievaleval"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	corpus := flag.String("corpus", "", "synthetic corpus JSON path")
	k := flag.Int("k", 3, "top-k")
	flag.Parse()
	if *corpus == "" || flag.NArg() != 0 {
		return errors.New("provide -corpus and optional -k")
	}
	file, err := os.Open(*corpus)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1048577))
	if err != nil {
		return err
	}
	if len(raw) > 1048576 {
		return errors.New("corpus exceeds 1 MiB")
	}
	var c retrievaleval.Corpus
	// Strict input prevents misspelled ACL or relevance fields silently changing metrics.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing corpus input")
	}
	report, err := retrievaleval.Evaluate(context.Background(), c, *k, "synthetic-character-bigram-v1", retrievaleval.Bigram)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(report); err != nil {
		return err
	}
	if report.Unauthorized != 0 {
		return errors.New("unauthorized retrieval detected")
	}
	return nil
}
