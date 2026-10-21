// cpa-session-capture bridges completed JSONL host records to shared delivery.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"cpa-session-archive/capture"
	"cpa-session-archive/record"
)

type input struct {
	Version int           `json:"version"`
	Record  record.Record `json:"record"`
}

func run(in io.Reader, out io.Writer, config capture.CaptureConfig) error {
	producer := capture.NewProducer()
	defer producer.Shutdown()
	if err := producer.Configure(config); err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = producer.Drain(ctx)
	}()
	scanner := bufio.NewScanner(in)
	// Three Base64 payloads plus metadata; independent of an untrusted input line.
	scanner.Buffer(make([]byte, 64<<10), config.MaxBodyBytes*4+(1<<20))
	encoder := json.NewEncoder(out)
	if err := encoder.Encode(map[string]any{"version": 1, "ready": true}); err != nil {
		return err
	}
	for scanner.Scan() {
		var message input
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Version != 1 {
			return fmt.Errorf("invalid capture input")
		}
		accepted := producer.Submit(message.Record) == nil
		if err := encoder.Encode(map[string]any{"version": 1, "accepted": accepted, "capture": producer.CaptureStatus()}); err != nil {
			return err
		}
	}
	if scanner.Err() != nil {
		return fmt.Errorf("capture input read failed")
	}
	return nil
}

func main() {
	config := capture.DefaultCaptureConfig()
	flag.StringVar(&config.Endpoint, "endpoint", "http://127.0.0.1:8318", "Archive collector endpoint")
	flag.StringVar(&config.SpoolDir, "spool-dir", "", "Private absolute outbox directory")
	flag.IntVar(&config.MaxBodyBytes, "max-body-bytes", 64<<20, "Maximum retained bytes per payload")
	flag.Parse()
	if config.MaxBodyBytes < 1 || config.MaxBodyBytes > 64<<20 {
		fmt.Fprintln(os.Stderr, "invalid capture body limit")
		os.Exit(1)
	}
	if err := run(os.Stdin, os.Stdout, config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
