// This binary implements a remote persistent worker for use in runner_test.go

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"os"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/util/log"
)

var (
	chunkSize        = flag.Int("response_chunk_size", 0, "Maximum response write size.")
	noise            = flag.Bool("stdout_noise", false, "Emit invalid protocol data before the response.")
	truncate         = flag.Bool("truncate_response", false, "Exit halfway through a response.")
	gate             = flag.String("response_gate", "", "Wait for this file before responding; create .ready after reading the request.")
	persistentWorker = flag.Bool("persistent_worker", false, "Persistent worker flag.")

	protocol       = flag.String("protocol", "proto", "Serialization protocol: 'json' or 'proto'.")
	responseBase64 = flag.String("response_base64", "", "Base64-encoded response to return for every request. Includes varint length prefix (for proto responses).")
	failWithStderr = flag.String("fail_with_stderr", "", "If non-empty, the worker will crash upon receiving the first request, printing the given message to stderr.")
)

func main() {
	flag.Parse()

	if !*persistentWorker {
		panic("Expected --persistent_worker flag, but was not set.")
	}

	resBytes, err := base64.StdEncoding.DecodeString(*responseBase64)
	if err != nil {
		panic(err)
	}

	var br io.ByteReader
	var dec *json.Decoder
	if *protocol == "json" {
		dec = json.NewDecoder(os.Stdin)
	} else {
		br = bufio.NewReader(os.Stdin)
	}

	for {
		// Note: Logging goes to stderr, so it doesn't mess with the persistent
		// worker's output.
		log.Info("[worker] Waiting for request...")
		if *protocol == "json" {
			if err := readJSONRequest(dec); err != nil {
				panic(err)
			}
		} else {
			if err := readProtoRequest(br); err != nil {
				panic(err)
			}
		}

		if *failWithStderr != "" {
			log.Infof("[worker] --fail_with_stderr was set; failing now and exiting.")
			os.Stderr.Write([]byte(*failWithStderr + "\n"))
			os.Exit(1)
		}

		log.Info("[worker] Got request! Sending response...")

		if *gate != "" {
			if err := os.WriteFile(*gate+".ready", nil, 0600); err != nil {
				panic(err)
			}
			for {
				if _, err := os.Stat(*gate); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		if *noise {
			_, _ = os.Stdout.Write([]byte("worker debug noise\n"))
		}
		out := resBytes
		if *truncate {
			out = out[:len(out)/2]
		}
		for len(out) > 0 {
			n := len(out)
			if *chunkSize > 0 && n > *chunkSize {
				n = *chunkSize
			}
			if _, err := os.Stdout.Write(out[:n]); err != nil {
				panic(err)
			}
			out = out[n:]
		}
		if *truncate {
			os.Exit(0)
		}
		_, err = os.Stdout.Write(nil)
		if err != nil {
			panic(err)
		}

		log.Info("[worker] Sent response!")
	}
}

func readProtoRequest(r io.ByteReader) error {
	reqSizeBytes, err := binary.ReadUvarint(r)
	if err != nil {
		return err
	}
	reqBytes := make([]byte, reqSizeBytes)
	for i := 0; i < int(reqSizeBytes); i++ {
		reqBytes[i], err = r.ReadByte()
		if err != nil {
			return err
		}
	}
	return nil
}

func readJSONRequest(decoder *json.Decoder) error {
	raw := json.RawMessage{}
	return decoder.Decode(&raw)
}
