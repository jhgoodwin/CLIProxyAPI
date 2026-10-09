//go:build linux && cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
)

// importPrices validates and saves a local LiteLLM snapshot. Fetching and choosing
// the source revision stays an explicit administrator action, outside request handling.
func importPrices(source, destination string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("close pricing source: %v", errClose)
		}
	}()
	b, err := io.ReadAll(io.LimitReader(f, 32*1024*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 32*1024*1024 {
		return errors.New("pricing snapshot exceeds 32 MiB")
	}
	var entries map[string]json.RawMessage
	if err = json.Unmarshal(b, &entries); err != nil {
		return err
	}
	valid := 0
	for name, raw := range entries {
		var row map[string]json.RawMessage
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		if _, ok := row["input_cost_per_token"]; !ok {
			continue
		}
		if _, ok := row["output_cost_per_token"]; !ok {
			continue
		}
		if _, err = priceDecimal(row["input_cost_per_token"]); err != nil {
			return fmt.Errorf("%s input: %w", name, err)
		}
		if _, err = priceDecimal(row["output_cost_per_token"]); err != nil {
			return fmt.Errorf("%s output: %w", name, err)
		}
		valid++
	}
	if valid == 0 {
		return errors.New("snapshot contains no valid text token prices")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = out.Write(b); err == nil {
		err = out.Sync()
	}
	errClose := out.Close()
	if err == nil {
		err = errClose
	}
	if err != nil {
		_ = os.Remove(destination)
	}
	return err
}
